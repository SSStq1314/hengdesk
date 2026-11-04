package authz

import (
	"context"
	"errors"
	"github.com/SSStq1314/hengdesk/server/internal/policy"

	"entgo.io/ent/dialect/sql"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/acl"
	"github.com/SSStq1314/hengdesk/server/internal/ent/deptclosure"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// Subject ACL 主体。
type Subject struct {
	Type            string `json:"type"` // user / dept / role
	ID              int    `json:"id"`
	IncludeChildren bool   `json:"include_children"`
}

// ErrBadSubject 主体不合法。
var ErrBadSubject = errors.New("bad acl subject")

// Grant 授予或覆盖一条 ACL。bits 会被规范化（高位蕴含低位）。bits=0 等同撤销。
func (s *Service) Grant(ctx context.Context, orgID, actorID int, resType, resID string, sub Subject, bits int) error {
	bits = rbac.Normalize(bits)
	st := acl.SubjectType(sub.Type)
	if acl.SubjectTypeValidator(st) != nil || sub.ID <= 0 {
		return ErrBadSubject
	}
	if sub.Type != "dept" {
		sub.IncludeChildren = false
	}
	if bits == 0 {
		_, err := s.DB.ACL.Delete().Where(acl.ResourceType(resType), acl.ResourceID(resID),
			acl.SubjectTypeEQ(st), acl.SubjectID(sub.ID)).Exec(ctx)
		return err
	}
	return s.DB.ACL.Create().SetOrgID(orgID).SetResourceType(resType).SetResourceID(resID).
		SetSubjectType(st).SetSubjectID(sub.ID).SetIncludeChildren(sub.IncludeChildren).
		SetPerms(bits).SetCreatedBy(actorID).
		OnConflictColumns(acl.FieldResourceType, acl.FieldResourceID, acl.FieldSubjectType, acl.FieldSubjectID).
		Update(func(u *ent.ACLUpsert) { u.UpdatePerms().UpdateIncludeChildren().UpdateCreatedBy() }).
		Exec(ctx)
}

// subjectPredicate 「p 命中的 ACL 行」谓词：
//
//	(subject=user 且 id=本人) 或 (subject=role 且 id∈本人角色)
//	或 (subject=dept 且 id∈本人部门) 或 (subject=dept 且 include_children 且 id∈本人部门的上级)
func subjectPredicate(p *auth.Principal) func(*sql.Selector) {
	return func(sel *sql.Selector) {
		ors := []*sql.Predicate{
			sql.And(sql.EQ(sel.C(acl.FieldSubjectType), "user"), sql.EQ(sel.C(acl.FieldSubjectID), p.ID)),
		}
		if len(p.RoleIDs) > 0 {
			ors = append(ors, sql.And(sql.EQ(sel.C(acl.FieldSubjectType), "role"), sql.In(sel.C(acl.FieldSubjectID), ints(p.RoleIDs)...)))
		}
		if len(p.DeptIDs) > 0 {
			ors = append(ors, sql.And(sql.EQ(sel.C(acl.FieldSubjectType), "dept"), sql.In(sel.C(acl.FieldSubjectID), ints(p.DeptIDs)...)))
			anc := sql.Select(deptclosure.FieldAncestorID).From(sql.Table(deptclosure.Table)).
				Where(sql.And(sql.EQ(deptclosure.FieldOrgID, p.OrgID), sql.In(deptclosure.FieldDescendantID, ints(p.DeptIDs)...)))
			ors = append(ors, sql.And(sql.EQ(sel.C(acl.FieldSubjectType), "dept"), sql.EQ(sel.C(acl.FieldIncludeChildren), true),
				sql.In(sel.C(acl.FieldSubjectID), anc)))
		}
		sel.Where(sql.Or(ors...))
	}
}

func ints(v []int) []any {
	out := make([]any, len(v))
	for i, x := range v {
		out[i] = x
	}
	return out
}

func bitPredicate(bit int) func(*sql.Selector) {
	return func(sel *sql.Selector) {
		sel.Where(sql.P(func(b *sql.Builder) {
			b.WriteString("(").Ident(sel.C(acl.FieldPerms)).WriteString(" & ").Arg(bit).WriteString(") <> 0")
		}))
	}
}

// Check 判断 p 对资源是否拥有权限位 bit。
func (s *Service) Check(ctx context.Context, p *auth.Principal, resType, resID string, bit int) (bool, error) {
	return s.DB.ACL.Query().Where(acl.OrgID(p.OrgID), acl.ResourceType(resType), acl.ResourceID(resID),
		subjectPredicate(p), bitPredicate(bit)).Exist(ctx)
}

// VisibleSelector 返回「p 拥有 bit 的资源 ID」子查询，供列表查询下推到 SQL：
//
//	q.Where(func(s *sql.Selector) { s.Where(sql.In(s.C("id"), authz.VisibleSelector(p, "kb", rbac.BitView))) })
func VisibleSelector(p *auth.Principal, resType string, bit int) *sql.Selector {
	t := sql.Table(acl.Table)
	sel := sql.Select(t.C(acl.FieldResourceID)).From(t)
	sel.Where(sql.And(sql.EQ(t.C(acl.FieldOrgID), p.OrgID), sql.EQ(t.C(acl.FieldResourceType), resType)))
	subjectPredicate(p)(sel)
	bitPredicate(bit)(sel)
	return sel
}

// VisibleIDs 直接返回 p 拥有 bit 的资源 ID 列表（小数据量场景）。
func (s *Service) VisibleIDs(ctx context.Context, p *auth.Principal, resType string, bit int) ([]string, error) {
	return s.DB.ACL.Query().Where(acl.OrgID(p.OrgID), acl.ResourceType(resType), subjectPredicate(p), bitPredicate(bit)).
		Unique(true).Select(acl.FieldResourceID).Strings(ctx)
}

// ToolAccess 工具可用性的统一入口：先过平台策略（policy.Decide：启用、非禁止、非待复核、未下线、服务器启用、同组织），
// 再要求调用者对该工具有 use 位（成员 / 部门含下级 / 角色）。管理员也不例外：没有授权就不可用。
func (s *Service) ToolAccess(ctx context.Context, p *auth.Principal, t *ent.Tool) (policy.Decision, error) {
	if p == nil || t == nil {
		return policy.Decision{Reason: policy.ReasonDisabled}, nil
	}
	var srv *ent.McpServer
	if t.ServerID != nil {
		v, err := s.DB.McpServer.Get(ctx, *t.ServerID)
		if err != nil && !ent.IsNotFound(err) {
			return policy.Decision{}, err
		}
		srv = v
	}
	d := policy.Decide(p.OrgID, t, srv)
	if !d.Usable {
		return d, nil
	}
	ok, err := s.Check(ctx, p, rbac.ResTool, t.Key, rbac.BitUse)
	if err != nil {
		return policy.Decision{}, err
	}
	if !ok {
		return policy.Decision{Reason: "no_acl"}, nil
	}
	return d, nil
}

// ToolUsable 只关心能否调用（不关心执行方式）。
func (s *Service) ToolUsable(ctx context.Context, p *auth.Principal, t *ent.Tool) (bool, error) {
	d, err := s.ToolAccess(ctx, p, t)
	return d.Usable, err
}
