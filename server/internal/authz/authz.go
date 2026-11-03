// Package authz 统一授权层：RBAC 权限点 + 部门范围 + 资源 ACL。
//
// 硬规则：
//   - 未登录 401；已登录但无权限、越权访问他人资源（IDOR）、资源不存在，一律 404，不暴露资源是否存在。
//   - 未知角色、未绑定部门的部门管理员授予，一律不给权限（fail-closed）。
package authz

import (
	"context"
	"net/http"

	"github.com/SSStq1314/hengdesk/server/internal/audit"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/deptclosure"
	"github.com/SSStq1314/hengdesk/server/internal/ent/membership"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// ErrNotFound 统一的越权/不存在响应。
var ErrNotFound = echo.NewHTTPError(http.StatusNotFound, "资源不存在")

// ErrUnauthenticated 未登录。
var ErrUnauthenticated = echo.NewHTTPError(http.StatusUnauthorized, "未登录")

// Require 要求拥有权限点（组织范围或任一部门范围）。未登录 401，无权限 404。
func Require(perm rbac.Perm) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			p := auth.Current(c)
			if p == nil {
				return ErrUnauthenticated
			}
			if !p.Perms.HasAny(perm) {
				return Deny(c, "perm:"+string(perm))
			}
			return next(c)
		}
	}
}

// Deny 记录越权尝试审计后返回 404。审计写失败返回 503（同样不暴露资源是否存在）。
func Deny(c echo.Context, reason string) error {
	if err := audit.Record(c, audit.Entry{Action: "authz.denied", Outcome: "denied",
		ResourceType: "route", ResourceID: c.Request().Method + " " + c.Path(),
		Detail: map[string]any{"reason": reason}}); err != nil {
		return err
	}
	return ErrNotFound
}

// Service 需要查库的授权判断。
type Service struct{ DB *ent.Client }

// Subtree 返回 roots 及其全部下级部门 ID（经 dept_closure）。
func (s *Service) Subtree(ctx context.Context, orgID int, roots []int) ([]int, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	return s.DB.DeptClosure.Query().
		Where(deptclosure.OrgID(orgID), deptclosure.AncestorIDIn(roots...)).
		Unique(true).Select(deptclosure.FieldDescendantID).Ints(ctx)
}

// Ancestors 返回 depts 及其全部上级部门 ID。
func (s *Service) Ancestors(ctx context.Context, orgID int, depts []int) ([]int, error) {
	if len(depts) == 0 {
		return nil, nil
	}
	return s.DB.DeptClosure.Query().
		Where(deptclosure.OrgID(orgID), deptclosure.DescendantIDIn(depts...)).
		Unique(true).Select(deptclosure.FieldAncestorID).Ints(ctx)
}

// UserScope 权限 perm 下 p 可管理的用户范围谓词。组织范围返回本组织全部用户；
// 部门范围返回所在部门子树内的用户；都没有时返回 nil（调用方应 404）。
func (s *Service) UserScope(ctx context.Context, p *auth.Principal, perm rbac.Perm) ([]func(*ent.UserQuery), bool, error) {
	roots, orgWide := p.Perms.DeptRoots(perm)
	if orgWide {
		return nil, true, nil
	}
	if len(roots) == 0 {
		return nil, false, nil
	}
	depts, err := s.Subtree(ctx, p.OrgID, roots)
	if err != nil {
		return nil, false, err
	}
	return []func(*ent.UserQuery){func(q *ent.UserQuery) {
		q.Where(user.HasMembershipsWith(membership.DepartmentIDIn(depts...)))
	}}, true, nil
}

// VisibleUser 按权限范围取同组织用户；不在范围内一律返回 ErrNotFound。
func (s *Service) VisibleUser(c echo.Context, perm rbac.Perm, id int) (*ent.User, error) {
	ctx := c.Request().Context()
	p := auth.Current(c)
	scope, ok, err := s.UserScope(ctx, p, perm)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, Deny(c, "scope:"+string(perm))
	}
	q := s.DB.User.Query().Where(user.ID(id), user.OrgID(p.OrgID))
	for _, f := range scope {
		f(q)
	}
	u, err := q.WithMemberships(func(mq *ent.MembershipQuery) { mq.WithRole() }).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, Deny(c, "idor:user")
		}
		return nil, err
	}
	return u, nil
}

// RoleKeys 用户当前持有的角色键。
func RoleKeys(u *ent.User) []string {
	out := []string{}
	for _, m := range u.Edges.Memberships {
		if m.Edges.Role != nil {
			out = append(out, m.Edges.Role.Key)
		}
	}
	return out
}

// CanManageTarget 管理他人账号（停用、重置密码、强制下线、解锁）的附加规则：
// 目标持有特权角色（超级管理员 / 安全审计员）时，操作者必须拥有 RoleAssignPriv。
func CanManageTarget(p *auth.Principal, target *ent.User) bool {
	for _, k := range RoleKeys(target) {
		if r, ok := rbac.Lookup(k); ok && r.Privileged && !p.Perms.Has(rbac.RoleAssignPriv) {
			return false
		}
	}
	return true
}
