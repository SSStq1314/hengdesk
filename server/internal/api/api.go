// Package api 组装 /api/v1 路由。
package api

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
)

// Deps 依赖。
type Deps struct {
	DB    *ent.Client
	Audit audit.Writer
	Auth  *auth.Service
	Log   *slog.Logger
}

// Mount 挂载路由。
func Mount(e *echo.Echo, d Deps) {
	g := e.Group("/api/v1", d.Auth.Optional, audit.Middleware(d.Audit, auth.Actor, d.Log))
	d.Auth.Register(g)
	admin := g.Group("/admin", authz.RequireRole("admin", "auditor"))
	admin.GET("/audit-logs", func(c echo.Context) error {
		limit, _ := strconv.Atoi(c.QueryParam("limit"))
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		rows, err := d.DB.AuditLog.Query().Order(ent.Desc(auditlog.FieldID)).Limit(limit).All(c.Request().Context())
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]any{"items": rows})
	})
}
