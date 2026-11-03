package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
)

// alertHandlers 审计员站内通知（D-34）：未读的高风险审计事件。已读位置按用户存 Valkey（只是游标，不影响审计链）。
type alertHandlers struct {
	db *ent.Client
	r  redis.UniversalClient
}

type alertView struct {
	ID         int            `json:"id"`
	Ts         time.Time      `json:"ts"`
	ActorEmail string         `json:"actor_email"`
	IP         string         `json:"ip"`
	Detail     map[string]any `json:"detail"`
}

func alertReadKey(uid int) string { return "alerts:read:" + strconv.Itoa(uid) }

// list GET /admin/alerts → {unread, items}（最多 20 条未读，新的在前）
func (h *alertHandlers) list(c echo.Context) error {
	p := auth.Current(c)
	ctx := c.Request().Context()
	last, err := h.r.Get(ctx, alertReadKey(p.ID)).Int()
	if err != nil && err != redis.Nil {
		return auth.ErrUnavailable
	}
	q := h.db.AuditLog.Query().Where(auditlog.Action(auth.AlertAction), auditlog.IDGT(last),
		auditlog.Or(auditlog.OrgID(p.OrgID), auditlog.OrgIDIsNil()))
	n, err := q.Clone().Count(ctx)
	if err != nil {
		return err
	}
	rows, err := q.Order(ent.Desc(auditlog.FieldID)).Limit(20).All(ctx)
	if err != nil {
		return err
	}
	items := make([]alertView, 0, len(rows))
	for _, r := range rows {
		items = append(items, alertView{ID: r.ID, Ts: r.Ts, ActorEmail: r.ActorEmail, IP: r.IP, Detail: r.Detail})
	}
	return c.JSON(http.StatusOK, map[string]any{"unread": n, "items": items})
}

// markRead POST /admin/alerts/read {up_to_id}
func (h *alertHandlers) markRead(c echo.Context) error {
	var req struct {
		UpTo int `json:"up_to_id"`
	}
	if err := c.Bind(&req); err != nil || req.UpTo <= 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	p := auth.Current(c)
	ctx := c.Request().Context()
	last, err := h.r.Get(ctx, alertReadKey(p.ID)).Int()
	if err != nil && err != redis.Nil {
		return auth.ErrUnavailable
	}
	if req.UpTo > last {
		if err := h.r.Set(ctx, alertReadKey(p.ID), req.UpTo, 0).Err(); err != nil {
			return auth.ErrUnavailable
		}
	}
	return c.NoContent(http.StatusNoContent)
}
