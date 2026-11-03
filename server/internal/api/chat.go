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
	// 重试与故障转移（PR5）：主模型 + 备用模型依次尝试；每次尝试都先写审计 attempt（fail-closed），
	// 结束后写审计 result 和一条 usage_record（attempt 递增）。只有首字节之前的连接错误 / 429 / 5xx 会重试；
	// 预算预检查只做一次，失败的尝试没有产生用量（按 0 记账），所以预算只计实际成功那次的用量。
	targets := append([]*gateway.Target{tgt}, h.gw.Fallbacks(ctx, tgt.Model)...)
	var (
		res     *gateway.ChatResponse
		ge      *gateway.Error
		started bool
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
			base := map[string]any{"model": tgt.Model.Name, "provider": t.Upstream.Name, "upstream_model": t.Config.UpstreamModel,
				"stream": req.Stream, "messages": len(req.Messages), "tools": len(req.Tools), "attempt": attempt}
			if t != tgt {
				base["fallback_model"] = t.Model.Name
			}
			if err := audit.Record(c, audit.Entry{Action: "model.call", Phase: "attempt", ResourceType: "model",
				ResourceID: tgt.Model.Name, Detail: base}); err != nil {
				if attempt == 1 {
					return err // fail-closed：审计写不进去就不调用模型
				}
				h.log.Error("model call audit attempt write failed, stop retrying", "model", tgt.Model.Name, "attempt", attempt)
				break loop // 之前尝试的错误照常返回
			}
			callStart := time.Now()
			if req.Stream {
				res, ge, started = h.stream(c, &req, t)
			} else {
				res, ge = h.gw.Client.Chat(egressCtxAudited(ctx, c, "model.chat"), &req, t.Upstream, t.Config)
			}
			h.finish(c, tgt, base, callStart, res, ge)
			h.recordUsage(c, p, t, req.Stream, attempt, callStart, res, ge)
			if !gateway.ShouldRetry(ge, started) {
				break loop
			}
			if try+1 == gateway.Retry.PerTarget && ti+1 == len(targets) {
				break loop
			}
		}
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
	return c.JSON(http.StatusOK, chatResult{ID: res.ID, Model: tgt.Model.Name, Message: res.Message,
		FinishReason: res.FinishReason, Usage: res.Usage, Warnings: res.Warnings})
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
func (h *chatHandlers) recordUsage(c echo.Context, p *auth.Principal, tgt *gateway.Target, stream bool, attempt int, start time.Time, res *gateway.ChatResponse, ge *gateway.Error) {
	ctx := context.WithoutCancel(c.Request().Context())
	rec := usage.Record{OrgID: p.OrgID, UserID: p.ID, DeptID: usage.PrimaryDept(ctx, h.gw.DB, p.ID), Model: tgt.Model,
		ProviderID: tgt.Model.ProviderID, RequestID: c.Response().Header().Get(echo.HeaderXRequestID),
		Purpose: "chat", Stream: stream, Attempt: attempt, Latency: time.Since(start)}
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
func (h *chatHandlers) stream(c echo.Context, req *gateway.ChatRequest, tgt *gateway.Target) (*gateway.ChatResponse, *gateway.Error, bool) {
	w := c.Response()
	started := false
	start := func() {
		if started {
			return
		}
		started = true
		w.Header().Set(echo.HeaderContentType, "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
	}
	send := func(event string, v any) error {
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
		return send("delta", d)
	})
	if ge != nil {
		if !started {
			return nil, ge, false
		}
		_ = send("error", map[string]any{"error": ge})
		return nil, ge, true
	}
	start()
	if err := send("done", chatResult{ID: res.ID, Model: tgt.Model.Name, Message: res.Message,
		FinishReason: res.FinishReason, Usage: res.Usage, Warnings: res.Warnings}); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		h.log.Debug("sse write failed", "err", err)
	}
	return res, nil, true
}
