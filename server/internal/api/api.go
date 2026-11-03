// Package api 组装 /api/v1 路由。
package api

import (
	"log/slog"
	"strconv"

	"github.com/SSStq1314/hengdesk/server/internal/egress"

	"github.com/SSStq1314/hengdesk/server/internal/budget"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// Deps 依赖。
type Deps struct {
	DB    *ent.Client
	Audit audit.Writer
	Auth  *auth.Service
	Log   *slog.Logger
	// System 管理后台状态提示（审计员未指派、备份盘空间不足等）。
	System SystemConfig
	// Gateway 模型网关；为 nil 时用默认 HTTP 客户端且不配置主密钥（不能保存或使用上游密钥）。
	Gateway *gateway.Service
	// Budget 预算服务；为 nil 时用 Auth.Sessions.R（Valkey）构造。
	Budget *budget.Service
	// Egress 统一出站客户端（FR-27）；为 nil 时按 DB 白名单构造。所有对外 HTTP 必须经过它。
	Egress       *egress.Client
	EgressPolicy *egress.DBPolicy
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

	g := e.Group("/api/v1", d.Auth.Optional, audit.WithWriter(d.Audit, auth.Actor), audit.Middleware(d.Audit, auth.Actor, d.Log), auth.CSRF)
	d.Auth.Register(g)
	admin := g.Group("/admin")

	ah := &auditHandlers{db: d.DB, retentionDays: d.System.AuditRetentionDays, now: d.System.Now}
	admin.GET("/audit-logs", ah.list, authz.Require(rbac.AuditRead), audit.Sensitive("audit.view"))
	admin.GET("/audit-logs/verify", ah.verify, authz.Require(rbac.AuditRead), audit.Sensitive("audit.verify"))
	admin.GET("/audit-logs/retention", ah.retention, authz.Require(rbac.AuditRead))
	alh := &alertHandlers{db: d.DB, r: d.Auth.Sessions.R}
	admin.GET("/alerts", alh.list, authz.Require(rbac.AuditRead))
	admin.POST("/alerts/read", alh.markRead, authz.Require(rbac.AuditRead))
	admin.GET("/audit-logs/export", ah.export, authz.Require(rbac.AuditExport), audit.Sensitive("audit.export"))

	// 账号安全（强制下线、停用、重置密码、解锁）：组织范围 MemberManage，目标为特权角色时还需 RoleAssignPriv。
	d.Auth.RegisterAdmin(admin.Group("", authz.Require(rbac.MemberManage)))

	sh := &systemHandlers{db: d.DB, cfg: d.System}
	admin.GET("/system/status", sh.status, authz.Require(rbac.SettingsManage))
	admin.GET("/system/tls", sh.tls, authz.Require(rbac.SettingsManage))
	admin.POST("/system/master-key/ack", sh.ackMasterKey, authz.Require(rbac.SettingsManage))

	mh := &memberHandlers{db: d.DB, az: az, auth: d.Auth}
	admin.GET("/users", mh.list, authz.Require(rbac.MemberRead))
	admin.GET("/users/:id", mh.get, authz.Require(rbac.MemberRead))
	admin.POST("/users", mh.create, authz.Require(rbac.MemberManage))
	admin.PATCH("/users/:id", mh.update, authz.Require(rbac.MemberManage))

	dh := &deptHandlers{db: d.DB, az: az}
	admin.GET("/departments", dh.list, authz.Require(rbac.DeptRead))
	admin.POST("/departments", dh.create, authz.Require(rbac.DeptManage))
	admin.POST("/departments/import", dh.importCSV, authz.Require(rbac.DeptManage))
	admin.PATCH("/departments/:id", dh.rename, authz.Require(rbac.DeptManage))
	admin.DELETE("/departments/:id", dh.remove, authz.Require(rbac.DeptManage))

	if d.Egress == nil || d.EgressPolicy == nil {
		d.Egress, d.EgressPolicy = egress.New(d.DB, d.Audit, d.Log)
	}
	gw := d.Gateway
	if gw == nil {
		gw = &gateway.Service{DB: d.DB, Client: &gateway.Client{HTTP: d.Egress.HTTP()}}
	}
	bs := d.Budget
	if bs == nil {
		bs = &budget.Service{DB: d.DB, R: d.Auth.Sessions.R, Log: d.Log}
	}
	mdh := &modelHandlers{db: d.DB, gw: gw, bs: bs, eg: d.Egress}
	eh := &egressHandlers{db: d.DB, client: d.Egress, policy: d.EgressPolicy}
	admin.GET("/egress/rules", eh.listRules, authz.Require(rbac.EgressManage))
	admin.POST("/egress/rules", eh.createRule, authz.Require(rbac.EgressManage))
	admin.PATCH("/egress/rules/:id", eh.updateRule, authz.Require(rbac.EgressManage))
	admin.DELETE("/egress/rules/:id", eh.deleteRule, authz.Require(rbac.EgressManage))
	admin.POST("/egress/check", eh.check, authz.Require(rbac.EgressManage))
	admin.GET("/egress/logs", eh.logs, authz.Require(rbac.EgressLogRead))
	admin.GET("/model-providers", mdh.listProviders, authz.Require(rbac.ModelManage))
	admin.POST("/model-providers", mdh.createProvider, authz.Require(rbac.ModelManage))
	admin.PATCH("/model-providers/:id", mdh.updateProvider, authz.Require(rbac.ModelManage))
	admin.DELETE("/model-providers/:id", mdh.deleteProvider, authz.Require(rbac.ModelManage))
	admin.GET("/models", mdh.listModels, authz.Require(rbac.ModelManage))
	admin.POST("/models", mdh.createModel, authz.Require(rbac.ModelManage))
	admin.PATCH("/models/:id", mdh.updateModel, authz.Require(rbac.ModelManage))
	admin.DELETE("/models/:id", mdh.deleteModel, authz.Require(rbac.ModelManage))
	admin.POST("/model-providers/:id/test", mdh.testProvider, authz.Require(rbac.ModelManage))
	admin.POST("/models/:id/default", mdh.setDefault, authz.Require(rbac.ModelManage))
	admin.POST("/models/:id/test", mdh.testModel, authz.Require(rbac.ModelManage))
	admin.GET("/models/:id/acl", mdh.listACL, authz.Require(rbac.ModelManage))
	admin.POST("/models/:id/acl", mdh.grantACL, authz.Require(rbac.ModelManage))
	admin.DELETE("/models/:id/acl/:type/:sid", mdh.revokeACL, authz.Require(rbac.ModelManage))
	admin.GET("/usage/summary", mdh.usageSummary, authz.Require(rbac.ModelManage))
	admin.GET("/usage/records", mdh.usageRecords, authz.Require(rbac.ModelManage))
	g.GET("/models", func(c echo.Context) error { return mdh.myModels(c, az) })
	g.GET("/usage/me", mdh.myUsage)
	ch := &chatHandlers{gw: gw, az: az, log: d.Log, bs: bs}
	g.POST("/chat/completions", ch.complete)

	bh := &budgetHandlers{db: d.DB, bs: bs}
	admin.GET("/budgets", bh.list, authz.Require(rbac.BudgetManage))
	admin.POST("/budgets", bh.create, authz.Require(rbac.BudgetManage))
	admin.PUT("/budgets/:id", bh.update, authz.Require(rbac.BudgetManage))
	admin.DELETE("/budgets/:id", bh.remove, authz.Require(rbac.BudgetManage))
	admin.POST("/budgets/reconcile", bh.reconcile, authz.Require(rbac.BudgetManage))
	g.GET("/budgets/me", bh.mine)
	nh := &notificationHandlers{db: d.DB}
	g.GET("/notifications", nh.list)
	g.POST("/notifications/read", nh.markRead)

	mountEdition(g, d.Auth)

	rh := &roleHandlers{db: d.DB, az: az}
	admin.GET("/roles", rh.matrix, authz.Require(rbac.RoleRead))
	admin.POST("/users/:id/roles", rh.assign, authz.Require(rbac.RoleAssign))
	admin.DELETE("/users/:id/roles/:role", rh.revoke, authz.Require(rbac.RoleAssign))
}
