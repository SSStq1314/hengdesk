package authz

import (
	"context"
	"errors"
	"slices"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent/membership"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// ErrOutsideSubtree 部门管理员试图把资源共享到本部门子树之外。
var ErrOutsideSubtree = errors.New("acl: 只有组织管理员可以跨部门子树共享")

// createPermFor 资源类型对应的创建权限点；共享范围以创建权限的范围为准。
func createPermFor(resType string) (rbac.Perm, bool) {
	switch resType {
	case rbac.ResKB:
		return rbac.KBCreate, true
	case rbac.ResAgent:
		return rbac.AgentCreate, true
	}
	return "", false
}

// InitShare 资源创建后的默认授权（D-33 第 5 条）：
//   - 创建者本人获得 manage（蕴含 edit/use/view）；
//   - 创建者的创建权限是部门范围（部门管理员）时，默认共享给其管辖部门及全部下级部门（view+use）；
//   - 组织范围（组织管理员 / 超级管理员）创建时默认只有本人，再由其显式共享。
func (s *Service) InitShare(ctx context.Context, p *auth.Principal, resType, resID string) error {
	perm, ok := createPermFor(resType)
	if !ok {
		return ErrBadSubject
	}
	roots, orgWide := p.Perms.DeptRoots(perm)
	if !orgWide && len(roots) == 0 {
		return ErrOutsideSubtree
	}
	if err := s.Grant(ctx, p.OrgID, p.ID, resType, resID, Subject{Type: "user", ID: p.ID}, rbac.BitManage); err != nil {
		return err
	}
	for _, d := range roots {
		if err := s.Grant(ctx, p.OrgID, p.ID, resType, resID, Subject{Type: "dept", ID: d, IncludeChildren: true}, rbac.BitUse); err != nil {
			return err
		}
	}
	return nil
}

// CanShareTo 判断 p 能否把 resType 资源共享给 sub：
// 组织范围创建权限可共享给本组织任何主体；部门范围只能共享给子树内的部门或子树内的成员，不能共享给角色（角色是组织级主体）。
func (s *Service) CanShareTo(ctx context.Context, p *auth.Principal, resType string, sub Subject) (bool, error) {
	perm, ok := createPermFor(resType)
	if !ok {
		return false, nil
	}
	roots, orgWide := p.Perms.DeptRoots(perm)
	if orgWide {
		return true, nil
	}
	if len(roots) == 0 {
		return false, nil
	}
	tree, err := s.Subtree(ctx, p.OrgID, roots)
	if err != nil {
		return false, err
	}
	switch sub.Type {
	case "dept":
		return slices.Contains(tree, sub.ID), nil
	case "user":
		return s.DB.User.Query().Where(user.ID(sub.ID), user.OrgID(p.OrgID),
			user.HasMembershipsWith(membership.DepartmentIDIn(tree...))).Exist(ctx)
	}
	return false, nil
}

// Share 带范围校验的授权：越出子树返回 ErrOutsideSubtree（调用方应记 authz.denied 审计）。
func (s *Service) Share(ctx context.Context, p *auth.Principal, resType, resID string, sub Subject, bits int) error {
	ok, err := s.CanShareTo(ctx, p, resType, sub)
	if err != nil {
		return err
	}
	if !ok {
		return ErrOutsideSubtree
	}
	return s.Grant(ctx, p.OrgID, p.ID, resType, resID, sub, bits)
}
