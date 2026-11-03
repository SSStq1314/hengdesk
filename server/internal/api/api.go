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
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// Deps 依赖。
type Deps struct {
	DB    *ent.Client
	Audit audit.Writer
	Auth  *auth.Service
	Log   *slog.Logger
}

// Mount 挂载路由。所有 /admin 接口都经统一授权层：未登录 401，无权限或越权 404。
func Mount(e *echo.Echo, d Deps) {
	az := &authz.Service{DB: d.DB}
	d.Auth.Target = func(c echo.Context) (*ent.User, error) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			return nil, authz.Deny(c, "bad_id")
		}
		u, err := az.VisibleUser(c, rbac.MemberManage, id)
		if err != nil {
			return nil, err
		}
		if !authz.CanManageTarget(auth.Current(c), u) {
			return nil, authz.Deny(c, "target_priv")
		}
		return u, nil
	}
	d.Auth.GuardDisable = guardDisable(d.DB)

	g := e.Group("/api/v1", d.Auth.Optional, audit.Middleware(d.Audit, auth.Actor, d.Log), auth.CSRF)
	d.Auth.Register(g)
	admin := g.Group("/admin")

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
	}, authz.Require(rbac.AuditRead))

	// 账号安全（强制下线、停用、重置密码、解锁）：组织范围 MemberManage，目标为特权角色时还需 RoleAssignPriv。
	d.Auth.RegisterAdmin(admin.Group("", authz.Require(rbac.MemberManage)))

	rh := &roleHandlers{db: d.DB, az: az}
	admin.GET("/roles", rh.matrix, authz.Require(rbac.RoleRead))
	admin.POST("/users/:id/roles", rh.assign, authz.Require(rbac.RoleAssign))
	admin.DELETE("/users/:id/roles/:role", rh.revoke, authz.Require(rbac.RoleAssign))
}
