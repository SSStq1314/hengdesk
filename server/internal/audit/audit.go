// Package audit 提供审计写入与 fail-closed 中间件。
//
// 规则：审计写失败 => 拒绝执行业务操作（fail-closed）。
package audit

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
)

// Entry 一条审计事件。
type Entry struct {
	OrgID        int
	ActorID      int
	ActorEmail   string
	Action       string
	ResourceType string
	ResourceID   string
	Phase        string // attempt / result
	Outcome      string
	IP           string
	UserAgent    string
	RequestID    string
	TraceID      string
	// UpstreamTraceID 为空时取 context 中受信代理传入的值（D-41 第 1 条）。
	UpstreamTraceID string
	Detail          map[string]any
}

// Writer 审计写入接口。实现必须同步落盘，返回 nil 才表示已持久化。
type Writer interface {
	Write(ctx context.Context, e Entry) error
}

// ErrAuditUnavailable 审计不可用。
var ErrAuditUnavailable = errors.New("audit unavailable")

// ErrRejected 审计写入失败时返回给客户端的统一错误（不暴露资源是否存在）。
var ErrRejected = echo.NewHTTPError(http.StatusServiceUnavailable, "审计服务不可用，操作已拒绝")

// EntWriter 基于 ent 的审计写入。
type EntWriter struct{ C *ent.Client }

func (w EntWriter) Write(ctx context.Context, e Entry) error {
	if e.Action == "" {
		return errors.New("audit: action required")
	}
	if e.TraceID == "" {
		e.TraceID = TraceFrom(ctx)
	}
	if e.UpstreamTraceID == "" {
		e.UpstreamTraceID = UpstreamTraceFrom(ctx)
	}
	phase := auditlog.PhaseResult
	if e.Phase == "attempt" {
		phase = auditlog.PhaseAttempt
	}
	chainMu.Lock()
	defer chainMu.Unlock()
	for i := 0; i < chainRetries; i++ {
		prev, err := w.tip(ctx)
		if err != nil {
			return err
		}
		ts := time.Now().UTC().Truncate(time.Microsecond)
		h := canonical(prev, ts, e.OrgID, e.ActorID, e.ActorEmail, e.Action, e.ResourceType, e.ResourceID,
			string(phase), e.Outcome, e.IP, e.UserAgent, e.RequestID, e.TraceID, e.UpstreamTraceID, e.Detail)
		q := w.C.AuditLog.Create().SetTs(ts).SetAction(e.Action).SetPhase(phase).
			SetActorEmail(e.ActorEmail).SetResourceType(e.ResourceType).SetResourceID(e.ResourceID).
			SetOutcome(e.Outcome).SetIP(e.IP).SetUserAgent(e.UserAgent).
			SetRequestID(e.RequestID).SetTraceID(e.TraceID).SetUpstreamTraceID(e.UpstreamTraceID).SetPrevHash(prev).SetHash(h)
		if e.OrgID != 0 {
			q.SetOrgID(e.OrgID)
		}
		if e.ActorID != 0 {
			q.SetActorID(e.ActorID)
		}
		if e.Detail != nil {
			q.SetDetail(e.Detail)
		}
		err = q.Exec(ctx)
		if err == nil {
			return nil
		}
		if !ent.IsConstraintError(err) {
			return err
		}
	}
	return errors.New("audit: chain contention")
}

// ActorFunc 从请求上下文取当前操作者（未登录返回 0, ""）。
type ActorFunc func(c echo.Context) (orgID, userID int, email string)

// Middleware 对变更类请求（非 GET/HEAD/OPTIONS）先写 attempt 审计，写失败直接 503 且不执行 handler；
// handler 执行后写 result 审计（此时失败只记日志，因为操作已发生）。
func Middleware(w Writer, actor ActorFunc, lg *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			r := c.Request()
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				return next(c)
			}
			e := Entry{
				Action: "http." + r.Method, ResourceType: "route", ResourceID: c.Path(),
				Phase: "attempt", IP: c.RealIP(), UserAgent: r.UserAgent(),
				RequestID: c.Response().Header().Get(echo.HeaderXRequestID),
			}
			if actor != nil {
				e.OrgID, e.ActorID, e.ActorEmail = actor(c)
			}
			if err := w.Write(r.Context(), e); err != nil {
				lg.Error("audit write failed, rejecting request (fail-closed)", "err", err, "path", c.Path())
				return ErrRejected
			}
			herr := next(c)
			status := c.Response().Status
			var he *echo.HTTPError
			if errors.As(herr, &he) {
				status = he.Code
			}
			e.Phase, e.Outcome = "result", http.StatusText(status)
			if actor != nil {
				e.OrgID, e.ActorID, e.ActorEmail = actor(c)
			}
			// 结果审计不受客户端断开影响（停止生成时也要留痕）。
			if err := w.Write(context.WithoutCancel(r.Context()), e); err != nil {
				lg.Error("audit result write failed", "err", err, "path", c.Path())
			}
			return herr
		}
	}
}

const ctxWriter = "hd.audit.writer"

// WithWriter 把审计写入器放进请求上下文，供授权层等记录拒绝事件。
func WithWriter(w Writer, actor ActorFunc) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(ctxWriter, w)
			c.Set(ctxWriter+".actor", actor)
			return next(c)
		}
	}
}

// Record 用上下文中的写入器同步写一条审计，自动补齐操作者与请求信息。
// 没有写入器或写入失败都返回 ErrRejected（fail-closed，调用方应直接返回该错误）。
func Record(c echo.Context, e Entry) error {
	w, _ := c.Get(ctxWriter).(Writer)
	if w == nil {
		return ErrRejected
	}
	if a, _ := c.Get(ctxWriter + ".actor").(ActorFunc); a != nil && e.ActorID == 0 {
		e.OrgID, e.ActorID, e.ActorEmail = a(c)
	}
	r := c.Request()
	e.IP, e.UserAgent = c.RealIP(), r.UserAgent()
	e.RequestID = c.Response().Header().Get(echo.HeaderXRequestID)
	ctx := r.Context()
	if e.Phase == "result" {
		// 结果审计不受客户端断开影响；attempt 仍随请求取消而失败（fail-closed）。
		ctx = context.WithoutCancel(ctx)
	}
	if err := w.Write(ctx, e); err != nil {
		return ErrRejected
	}
	return nil
}

// Sensitive 敏感读取（导出、查看审计、下载原件）：GET 也先写 attempt 审计，写失败 503。
func Sensitive(action string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if err := Record(c, Entry{Action: action, Phase: "attempt", ResourceType: "route", ResourceID: c.Path(),
				Detail: map[string]any{"query": c.QueryString()}}); err != nil {
				return err
			}
			return next(c)
		}
	}
}

// EntWriterFrom 取请求上下文中的审计写入器（供需要自行控制事务的业务使用）；没有时返回一个总是失败的写入器。
func EntWriterFrom(c echo.Context) Writer {
	if w, _ := c.Get(ctxWriter).(Writer); w != nil {
		return w
	}
	return failWriter{}
}

type failWriter struct{}

func (failWriter) Write(context.Context, Entry) error { return ErrAuditUnavailable }
