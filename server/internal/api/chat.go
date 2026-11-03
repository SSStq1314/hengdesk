package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

const maxChatBody = 8 << 20

// chatHandlers 统一内部 Chat API：POST /api/v1/chat/completions。
// 处理顺序：鉴权 → 模型解析 → 模型 ACL（无权限与不存在一律 404）→ 审计 attempt（写失败拒绝调用，fail-closed）
// → 调用上游（stream=true 时 SSE）→ 审计 result（含 usage、耗时、错误码）。
// 预算预检查与出站白名单在后续 PR 插入到审计 attempt 之前。
type chatHandlers struct {
	gw  *gateway.Service
	az  *authz.Service
	log *slog.Logger
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
	if req.Model == "" {
		return bad("model 必填")
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
		ok, err := h.az.Check(ctx, p, rbac.ResModel, tgt.Model.Name, rbac.BitUse)
		if err != nil {
			return err
		}
		if !ok {
			return authz.Deny(c, "model:acl")
		}
	}
	base := map[string]any{"model": tgt.Model.Name, "provider": tgt.Upstream.Name, "upstream_model": tgt.Config.UpstreamModel,
		"stream": req.Stream, "messages": len(req.Messages), "tools": len(req.Tools)}
	if err := audit.Record(c, audit.Entry{Action: "model.call", Phase: "attempt", ResourceType: "model",
		ResourceID: tgt.Model.Name, Detail: base}); err != nil {
		return err // fail-closed：审计写不进去就不调用模型
	}
	start := time.Now()
	var (
		res *gateway.ChatResponse
		ge  *gateway.Error
	)
	if req.Stream {
		res, ge = h.stream(c, &req, tgt)
	} else {
		res, ge = h.gw.Client.Chat(ctx, &req, tgt.Upstream, tgt.Config)
	}
	h.finish(c, tgt, base, start, res, ge)
	if req.Stream {
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

// stream SSE 输出：event: delta（增量）、event: done（聚合后的完整 assistant 消息与 usage，供多轮原样回传）、
// event: error（流开始后出错）。流开始前出错时直接返回 JSON 错误与对应状态码。
func (h *chatHandlers) stream(c echo.Context, req *gateway.ChatRequest, tgt *gateway.Target) (*gateway.ChatResponse, *gateway.Error) {
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
	res, ge := h.gw.Client.Stream(c.Request().Context(), req, tgt.Upstream, tgt.Config, func(d gateway.Delta) error {
		start()
		return send("delta", d)
	})
	if ge != nil {
		if !started {
			_ = gwError(c, ge)
			return nil, ge
		}
		_ = send("error", map[string]any{"error": ge})
		return nil, ge
	}
	start()
	if err := send("done", chatResult{ID: res.ID, Model: tgt.Model.Name, Message: res.Message,
		FinishReason: res.FinishReason, Usage: res.Usage, Warnings: res.Warnings}); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		h.log.Debug("sse write failed", "err", err)
	}
	return res, nil
}
