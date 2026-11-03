// Package audit 提供审计写入与 fail-closed 中间件。
//
// 规则：审计写失败 => 拒绝执行业务操作（fail-closed）。
package audit

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

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
	Detail       map[string]any
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
	phase := auditlog.PhaseResult
	if e.Phase == "attempt" {
		phase = auditlog.PhaseAttempt
	}
	q := w.C.AuditLog.Create().SetAction(e.Action).SetPhase(phase).
		SetActorEmail(e.ActorEmail).SetResourceType(e.ResourceType).SetResourceID(e.ResourceID).
		SetOutcome(e.Outcome).SetIP(e.IP).SetUserAgent(e.UserAgent).
		SetRequestID(e.RequestID).SetTraceID(e.TraceID)
	if e.OrgID != 0 {
		q.SetOrgID(e.OrgID)
	}
	if e.ActorID != 0 {
		q.SetActorID(e.ActorID)
	}
	if e.Detail != nil {
		q.SetDetail(e.Detail)
	}
	return q.Exec(ctx)
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
			if err := w.Write(r.Context(), e); err != nil {
				lg.Error("audit result write failed", "err", err, "path", c.Path())
			}
			return herr
		}
	}
}
