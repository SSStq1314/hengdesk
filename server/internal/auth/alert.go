package auth

import (
	"context"
	"log/slog"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
)

// 高风险事件（D-34）。站内通知：审计员后台按审计链中 action=AlertAction 的记录显示未读提醒；
// 邮件 / IM 等外部通道通过 Notifier 扩展，后续接入。
const (
	AlertAction          = "alert.high_risk"
	SeverityHigh         = "high"
	AlertBreakGlassLogin = "break_glass_login"
)

// Alert 高风险事件。
type Alert struct {
	Kind       string
	OrgID      int
	ActorID    int
	ActorEmail string
	IP         string
}

// Notifier 外部通知通道扩展点。实现应尽快返回；失败只记日志，不影响主流程（站内通知已由审计保证）。
type Notifier interface {
	Name() string
	Notify(ctx context.Context, a Alert) error
}

// RaiseAlert 写一条高风险审计（fail-closed），再尽力推送外部通知。
func (s *Service) RaiseAlert(c echo.Context, base audit.Entry, kind string) error {
	ctx := c.Request().Context()
	ae := base
	ae.Action, ae.Outcome = AlertAction, "success"
	ae.ResourceType, ae.ResourceID = "user", base.ActorEmail
	ae.Detail = map[string]any{"kind": kind, "severity": SeverityHigh}
	if err := s.Audit.Write(ctx, ae); err != nil {
		return audit.ErrRejected
	}
	a := Alert{Kind: kind, OrgID: base.OrgID, ActorID: base.ActorID, ActorEmail: base.ActorEmail, IP: base.IP}
	for _, n := range s.Notifiers {
		if err := n.Notify(ctx, a); err != nil {
			slog.Default().Warn("alert notify failed", "notifier", n.Name(), "kind", kind, "err", err)
		}
	}
	return nil
}
