package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/budget"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/usage"
)

const maxChatBody = 8 << 20

// chatHandlers 统一内部 Chat API：POST /api/v1/chat/completions。
// 处理顺序：鉴权 → 模型解析 → 模型 ACL（无权限与不存在一律 404）→ 审计 attempt（写失败拒绝调用，fail-closed）
// → 调用上游（stream=true 时 SSE）→ 审计 result（含 usage、耗时、错误码）。
// 预算预检查（PR3）在审计 attempt 之前；出站白名单在后续 PR 插入。
type chatHandlers struct {
	gw  *gateway.Service
	az  *authz.Service
	log *slog.Logger
	bs  *budget.Service
}

type chatResult struct {
	ID           string          `json:"id"`
	Model        string          `json:"model"`
	Message      gateway.Message `json:"message"`
	FinishReason string          `json:"finish_reason"`
	Usage        gateway.Usage   `json:"usage"`
	Warnings     []string        `json:"warnings,omitempty"`
	// FallbackFrom 本次由主模型切换到备用模型时为主模型名，Model 为实际使用的模型（D-39 第 7 条）。
	FallbackFrom string `json:"fallback_from,omitempty"`
}

func gwError(c echo.Context, e *gateway.Error) error {
	return c.JSON(e.Status, map[string]any{"error": e})
}

func (h *chatHandlers) complete(c echo.Context) error {
	p := auth.Current(c)
	if p == nil {
		return authz.ErrUnauthenticated
	}
	r := c.Request()
	r.Body = http.MaxBytesReader(c.Response(), r.Body, maxChatBody)
	var req gateway.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return bad("请求格式错误或超过 8 MiB")
	}
	ctx := r.Context()
	tgt, gerr := h.gw.Resolve(ctx, req.Model)
	if gerr != nil {
		if gerr.Code == gateway.CodeModelNotFound {
			return authz.Deny(c, "model:not_found")
		}
		return gwError(c, gerr)
	}
	if !p.Perms.Has(rbac.ModelManage) {
		ok, err := h.az.Check(ctx, p, rbac.ResModel, strconv.Itoa(tgt.Model.ID), rbac.BitUse)
		if err != nil {
			return err
		}
		if !ok {
			return authz.Deny(c, "model:acl")
		}
	}
	if err := budgetGate(c, h.bs, h.gw.DB, tgt.Model.ID, tgt.Model.Name); err != nil || c.Response().Committed {
		return err
	}
	res, ge, started, used, err := h.run(c, p, &req, tgt, apiHooks)
	if err != nil {
		return err
	}
	if req.Stream {
		if ge != nil && !started {
			_ = gwError(c, ge)
		}
		return nil
	}
	if ge != nil {
		return gwError(c, ge)
	}
	return c.JSON(http.StatusOK, chatResult{ID: res.ID, Model: used.Model.Name, Message: res.Message,
		FinishReason: res.FinishReason, Usage: res.Usage, Warnings: res.Warnings, FallbackFrom: fallbackFrom(used, tgt)})
}

// streamHooks 流式输出的事件名与回调：内部 Chat API 用 delta / done（chatResult）；
// 对话接口（第三块）用 token / done（消息），并通过 tap 累计已生成内容，用于停止或断开时落库。
type streamHooks struct {
	delta string
	tap   func(gateway.Delta)
	// prelude 流开始时（第一段增量之前）依次发出的事件，例如知识库引用。
	prelude []sseEvent
	done    func(res *gateway.ChatResponse, used, primary *gateway.Target) any
}

var apiHooks = streamHooks{delta: "delta", done: func(res *gateway.ChatResponse, used, primary *gateway.Target) any {
	return chatResult{ID: res.ID, Model: used.Model.Name, Message: res.Message,
		FinishReason: res.FinishReason, Usage: res.Usage, Warnings: res.Warnings, FallbackFrom: fallbackFrom(used, primary)}
}}

// run 调用模型（已完成模型解析、ACL 与预算预检之后）：主模型 + 有权限的备用模型依次尝试。
// err 非空表示 fail-closed 拒绝（未调用上游）；ge 为上游错误；started 表示流式已开始输出。
func (h *chatHandlers) run(c echo.Context, p *auth.Principal, req *gateway.ChatRequest, tgt *gateway.Target, hk streamHooks) (*gateway.ChatResponse, *gateway.Error, bool, *gateway.Target, error) {
	// 重试与故障转移（PR5）：主模型 + 备用模型依次尝试；每次尝试都先写审计 attempt（fail-closed），
	// 结束后写审计 result 和一条 usage_record（attempt 递增）。只有首字节之前的连接错误 / 429 / 5xx 会重试；
	// 预算预检查只做一次，失败的尝试没有产生用量（按 0 记账），所以预算只计实际成功那次的用量。
	fbs, err := h.usableFallbacks(c, p, tgt)
	if err != nil {
		return nil, nil, false, tgt, err
	}
	targets := append([]*gateway.Target{tgt}, fbs...)
	var (
		res     *gateway.ChatResponse
		ge      *gateway.Error
		started bool
		used    = tgt
		ctx     = c.Request().Context()
	)
	attempt := 0
loop:
	for ti, t := range targets {
		for try := 0; try < gateway.Retry.PerTarget; try++ {
			if attempt > 0 {
				select {
				case <-time.After(gateway.Retry.Backoff * time.Duration(attempt)):
				case <-ctx.Done():
					break loop
				}
			}
			attempt++
			base := map[string]any{"model": t.Model.Name, "provider": t.Upstream.Name, "upstream_model": t.Config.UpstreamModel,
				"stream": req.Stream, "messages": len(req.Messages), "tools": len(req.Tools), "attempt": attempt}
			// D-39 第 7 条：审计与用量记实际使用的模型，并标注「由 A 切换」。
			if t != tgt {
				base["fallback_from"] = tgt.Model.Name
				base["note"] = "由 " + tgt.Model.Name + " 切换"
			}
			if err := audit.Record(c, audit.Entry{Action: "model.call", Phase: "attempt", ResourceType: "model",
				ResourceID: t.Model.Name, Detail: base}); err != nil {
				if attempt == 1 {
					return nil, nil, false, tgt, err // fail-closed：审计写不进去就不调用模型
				}
				h.log.Error("model call audit attempt write failed, stop retrying", "model", tgt.Model.Name, "attempt", attempt)
				break loop // 之前尝试的错误照常返回
			}
			callStart := time.Now()
			used = t
			if req.Stream {
				res, ge, started = h.stream(c, req, t, tgt, hk)
			} else {
				res, ge = h.gw.Client.Chat(egressCtxAudited(ctx, c, "model.chat"), req, t.Upstream, t.Config)
			}
			h.finish(c, t, base, callStart, res, ge)
			h.recordUsage(c, p, t, fallbackFrom(t, tgt), req.Stream, attempt, callStart, res, ge)
			if !gateway.ShouldRetry(ge, started) {
				break loop
			}
			if try+1 == gateway.Retry.PerTarget && ti+1 == len(targets) {
				break loop
			}
		}
	}
	return res, ge, started, used, nil
}

type sseEvent struct {
	event string
	data  any
}

func fallbackFrom(used, primary *gateway.Target) string {
	if used == primary {
		return ""
	}
	return primary.Model.Name
}

// usableFallbacks D-39 第 6 条：只切换到调用人有使用权限的备用模型（与主模型同样的 ACL 判断，模型管理员不受限）。
// 一个都没有时返回空，重试耗尽后返回主模型的原错误。
func (h *chatHandlers) usableFallbacks(c echo.Context, p *auth.Principal, tgt *gateway.Target) ([]*gateway.Target, error) {
	ctx := c.Request().Context()
	all := h.gw.Fallbacks(ctx, tgt.Model)
	if p.Perms.Has(rbac.ModelManage) {
		return all, nil
	}
	out := make([]*gateway.Target, 0, len(all))
	for _, t := range all {
		ok, err := h.az.Check(ctx, p, rbac.ResModel, strconv.Itoa(t.Model.ID), rbac.BitUse)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, t)
		} else {
			h.log.Info("skip fallback model without use permission", "model", tgt.Model.Name, "fallback", t.Model.Name, "user", p.ID)
		}
	}
	return out, nil
}

// finish 写审计 result。此时调用已发生，写失败只记日志（与 audit.Middleware 的 result 一致）。
func (h *chatHandlers) finish(c echo.Context, tgt *gateway.Target, base map[string]any, start time.Time, res *gateway.ChatResponse, ge *gateway.Error) {
	d := map[string]any{}
	for k, v := range base {
		d[k] = v
	}
	d["latency_ms"] = time.Since(start).Milliseconds()
	outcome := "ok"
	if ge != nil {
		outcome = "error"
		d["error_code"], d["error"] = ge.Code, ge.Message
		if ge.UpstreamStatus != 0 {
			d["upstream_status"] = ge.UpstreamStatus
		}
		h.log.Warn("model call failed", "model", tgt.Model.Name, "provider", tgt.Upstream.Name, "code", ge.Code, "msg", ge.Message)
	} else if res != nil {
		d["finish_reason"], d["usage"], d["tool_calls"] = res.FinishReason, res.Usage, len(res.Message.ToolCalls)
		if len(res.Warnings) > 0 {
			d["warnings"] = res.Warnings
		}
	}
	if err := audit.Record(c, audit.Entry{Action: "model.call", Phase: "result", Outcome: outcome, ResourceType: "model",
		ResourceID: tgt.Model.Name, Detail: d}); err != nil {
		h.log.Error("model call audit result write failed", "model", tgt.Model.Name)
	}
}

// recordUsage 写 usage_records（计量与 PR3 预算的数据源）。调用已发生，写失败只记日志；
// 用 WithoutCancel 保证调用方断开后仍能记账。上游返回前就失败的调用记 0 token。
func (h *chatHandlers) recordUsage(c echo.Context, p *auth.Principal, tgt *gateway.Target, from string, stream bool, attempt int, start time.Time, res *gateway.ChatResponse, ge *gateway.Error) {
	ctx := context.WithoutCancel(c.Request().Context())
	rec := usage.Record{OrgID: p.OrgID, UserID: p.ID, DeptID: usage.PrimaryDept(ctx, h.gw.DB, p.ID), Model: tgt.Model,
		ProviderID: tgt.Model.ProviderID, RequestID: c.Response().Header().Get(echo.HeaderXRequestID),
		Purpose: "chat", Stream: stream, Attempt: attempt, Latency: time.Since(start), FallbackFrom: from,
		AgentID: usage.AgentIDFrom(c.Request().Context())}
	if ge != nil {
		rec.ErrorCode = ge.Code
		if ge.Usage != nil {
			rec.Usage = *ge.Usage
		}
	} else if res != nil {
		rec.Usage = res.Usage
	}
	ur, err := usage.Write(ctx, h.gw.DB, rec)
	if err != nil {
		h.log.Error("usage record write failed", "model", tgt.Model.Name, "err", err)
	}
	settle(ctx, h.bs, ur) // 流式调用在结束后才走到这里：按实际用量结算
}

// stream SSE 输出：event: delta（增量）、event: done（聚合后的完整 assistant 消息与 usage，供多轮原样回传）、
// event: error（流开始后出错）。流开始前出错时不写响应，返回 started=false，由调用方决定重试或返回 JSON 错误。
func (h *chatHandlers) stream(c echo.Context, req *gateway.ChatRequest, tgt, primary *gateway.Target, hk streamHooks) (*gateway.ChatResponse, *gateway.Error, bool) {
	w := c.Response()
	started := false
	var send func(event string, v any) error
	start := func() {
		if started {
			return
		}
		started = true
		if !w.Committed { // 工具循环的后续轮次：同一个流里接着输出
			w.Header().Set(echo.HeaderContentType, "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
		}
		for _, ev := range hk.prelude {
			_ = send(ev.event, ev.data)
		}
	}
	send = func(event string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return err
		}
		w.Flush()
		return nil
	}
	res, ge := h.gw.Client.Stream(egressCtxAudited(c.Request().Context(), c, "model.chat"), req, tgt.Upstream, tgt.Config, func(d gateway.Delta) error {
		start()
		if hk.tap != nil {
			hk.tap(d)
		}
		return send(hk.delta, d)
	})
	if ge != nil {
		if !started {
			return nil, ge, false
		}
		_ = send("error", map[string]any{"error": ge})
		return nil, ge, true
	}
	start()
	// done 返回 nil 表示调用方还要继续（工具循环），本轮不发 done。
	if v := hk.done(res, tgt, primary); v != nil {
		if err := send("done", v); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
			h.log.Debug("sse write failed", "err", err)
		}
	}
	return res, nil, true
}
