package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/department"
	"github.com/SSStq1314/hengdesk/server/internal/ent/membership"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// ErrLastSuperAdmin 至少保留 1 名超级管理员。
var ErrLastSuperAdmin = echo.NewHTTPError(http.StatusBadRequest, "系统至少保留 1 名启用状态的超级管理员")

type roleHandlers struct {
	db *ent.Client
	az *authz.Service
}

type roleView struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	DeptScoped  bool     `json:"dept_scoped"`
	Privileged  bool     `json:"privileged"`
}

// matrix GET /admin/roles：只读角色矩阵。
func (h *roleHandlers) matrix(c echo.Context) error {
	out := make([]roleView, 0, len(rbac.Builtin))
	for _, r := range rbac.Builtin {
		ps := make([]string, 0, len(r.Perms))
		for _, p := range r.Perms {
			ps = append(ps, string(p))
		}
		out = append(out, roleView{Key: r.Key, Name: r.Name, Permissions: ps, DeptScoped: r.DeptScoped, Privileged: r.Privileged})
	}
	perms := []string{}
	for _, p := range rbac.AllPerms() {
		perms = append(perms, string(p))
	}
	return c.JSON(http.StatusOK, map[string]any{"roles": out, "permissions": perms,
		"exclusive": rbac.Exclusive})
}

type assignReq struct {
	Role         string `json:"role"`
	DepartmentID int    `json:"department_id"`
}

// checkAssignable 操作者能否授予/撤销该角色。不能时返回 404（不暴露特权角色细节）。
func checkAssignable(c echo.Context, key string) (rbac.Role, error) {
	r, ok := rbac.Lookup(key)
	if !ok {
		return r, echo.NewHTTPError(http.StatusBadRequest, "未知角色")
	}
	p := auth.Current(c)
	if r.Privileged && !p.Perms.Has(rbac.RoleAssignPriv) {
		return r, authz.Deny(c, "assign_priv:"+key)
	}
	if !p.Perms.Has(rbac.RoleAssign) {
		return r, authz.Deny(c, "assign:"+key)
	}
	return r, nil
}

// lockSuperAdmins 在事务内锁住 super_admin 角色行，串行化「最后一名超级管理员」检查。
func lockSuperAdmins(ctx context.Context, tx *ent.Tx) error {
	return tx.Role.Update().Where(role.Key(rbac.SuperAdmin)).SetUpdatedAt(time.Now()).Exec(ctx)
}

// activeSuperAdmins 本组织启用状态的超级管理员人数。
func activeSuperAdmins(ctx context.Context, tx *ent.Tx, orgID int) (int, error) {
	return tx.User.Query().Where(user.OrgID(orgID), user.StatusEQ(user.StatusActive),
		user.HasMembershipsWith(membership.HasRoleWith(role.Key(rbac.SuperAdmin)))).Count(ctx)
}

// assign POST /admin/users/:id/roles
func (h *roleHandlers) assign(c echo.Context) error {
	var req assignReq
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	r, err := checkAssignable(c, req.Role)
	if err != nil {
		return err
	}
	id, _ := strconv.Atoi(c.Param("id"))
	u, err := h.az.VisibleUser(c, rbac.RoleAssign, id)
	if err != nil {
		return err
	}
	if !authz.CanManageTarget(auth.Current(c), u) {
		return authz.Deny(c, "target_priv")
	}
	if other, bad := rbac.Conflicts(authz.RoleKeys(u), r.Key); bad {
		o, _ := rbac.Lookup(other)
		return echo.NewHTTPError(http.StatusBadRequest, "三员分立："+r.Name+"与"+o.Name+"不能授予同一人")
	}
	ctx := c.Request().Context()
	var dept *int
	if r.DeptScoped {
		if req.DepartmentID == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, r.Name+"必须指定部门")
		}
		ok, err := h.db.Department.Query().Where(department.ID(req.DepartmentID), department.OrgID(u.OrgID)).Exist(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return authz.Deny(c, "idor:dept")
		}
		dept = &req.DepartmentID
	}
	ro, err := h.db.Role.Query().Where(role.Key(r.Key)).Only(ctx)
	if err != nil {
		return err
	}
	q := h.db.Membership.Query().Where(membership.UserID(u.ID), membership.RoleID(ro.ID))
	if dept != nil {
		q.Where(membership.DepartmentID(*dept))
	} else {
		q.Where(membership.DepartmentIDIsNil())
	}
	if exists, err := q.Exist(ctx); err != nil {
		return err
	} else if exists {
		return c.NoContent(http.StatusNoContent)
	}
	if err := recordRole(c, "role.assign", u, r.Key, dept); err != nil {
		return err
	}
	if err := h.db.Membership.Create().SetUserID(u.ID).SetRoleID(ro.ID).SetNillableDepartmentID(dept).Exec(ctx); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// revoke DELETE /admin/users/:id/roles/:role[?department_id=]
func (h *roleHandlers) revoke(c echo.Context) error {
	r, err := checkAssignable(c, c.Param("role"))
	if err != nil {
		return err
	}
	id, _ := strconv.Atoi(c.Param("id"))
	u, err := h.az.VisibleUser(c, rbac.RoleAssign, id)
	if err != nil {
		return err
	}
	if !authz.CanManageTarget(auth.Current(c), u) {
		return authz.Deny(c, "target_priv")
	}
	var dept *int
	if d, _ := strconv.Atoi(c.QueryParam("department_id")); d > 0 {
		dept = &d
	}
	// 审计先于变更（fail-closed）：审计写失败则不撤销。结果（成功 / 被拒）另由审计中间件记 result。
	if err := recordRole(c, "role.revoke", u, r.Key, dept); err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if r.Key == rbac.SuperAdmin {
		if err := lockSuperAdmins(ctx, tx); err != nil {
			return err
		}
	}
	del := tx.Membership.Delete().Where(membership.UserID(u.ID), membership.HasRoleWith(role.Key(r.Key)))
	if dept != nil {
		del.Where(membership.DepartmentID(*dept))
	}
	if _, err := del.Exec(ctx); err != nil {
		return err
	}
	if r.Key == rbac.SuperAdmin {
		n, err := activeSuperAdmins(ctx, tx, u.OrgID)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrLastSuperAdmin
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// recordRole 记录角色授予/撤销（含安全审计员指派），审计员可在审计日志中查看。
func recordRole(c echo.Context, action string, u *ent.User, key string, dept *int) error {
	d := map[string]any{"role": key, "target_email": u.Email}
	if dept != nil {
		d["department_id"] = *dept
	}
	if key == rbac.SecurityAuditor {
		d["auditor"] = true
	}
	return audit.Record(c, audit.Entry{Action: action, Phase: "attempt",
		ResourceType: "user", ResourceID: strconv.Itoa(u.ID), Detail: d})
}

// guardDisable 停用账号前：若目标是最后一名启用的超级管理员则拒绝。
func guardDisable(db *ent.Client) func(c echo.Context, u *ent.User) error {
	return func(c echo.Context, u *ent.User) error {
		ctx := c.Request().Context()
		isSA, err := db.Membership.Query().Where(membership.UserID(u.ID), membership.HasRoleWith(role.Key(rbac.SuperAdmin))).Exist(ctx)
		if err != nil || !isSA {
			return err
		}
		n, err := db.User.Query().Where(user.OrgID(u.OrgID), user.StatusEQ(user.StatusActive), user.IDNEQ(u.ID),
			user.HasMembershipsWith(membership.HasRoleWith(role.Key(rbac.SuperAdmin)))).Count(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrLastSuperAdmin
		}
		return nil
	}
}
