package api

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auditarchive"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
)

type archiveView struct {
	CheckpointID int            `json:"checkpoint_id"`
	Ts           time.Time      `json:"ts"`
	ActorEmail   string         `json:"actor_email"`
	Detail       map[string]any `json:"detail"`
}

// retention GET /admin/audit-logs/retention：保留期状态与最近 20 次归档（FR-40）。
// 归档本身只能由审计员在服务器上执行 `hengdesk audit archive`，接口只读。
func (h *auditHandlers) retention(c echo.Context) error {
	ctx := c.Request().Context()
	now := time.Now()
	if h.now != nil {
		now = h.now()
	}
	days := h.retentionDays
	if days <= 0 {
		days = 365
	}
	st, err := auditarchive.Check(ctx, h.db, days, now)
	if err != nil {
		return err
	}
	rows, err := h.db.AuditLog.Query().Where(auditlog.Action(audit.CheckpointAction)).
		Order(ent.Desc(auditlog.FieldID)).Limit(20).All(ctx)
	if err != nil {
		return err
	}
	items := make([]archiveView, 0, len(rows))
	for _, r := range rows {
		items = append(items, archiveView{CheckpointID: r.ID, Ts: r.Ts, ActorEmail: r.ActorEmail, Detail: r.Detail})
	}
	return c.JSON(http.StatusOK, map[string]any{"status": st, "warn_days": auditarchive.WarnDays, "archives": items})
}
