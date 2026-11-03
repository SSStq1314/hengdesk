package auth

import (
	"context"
	"slices"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/department"
	"github.com/SSStq1314/hengdesk/server/internal/ent/membership"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// oidcDenial 登录被拒绝的原因（写入审计，不对外暴露）。
type oidcDenial struct{ reason string }

func (d oidcDenial) Error() string { return "oidc denied: " + d.reason }

// domainAllowed 邮箱域名是否在白名单内；白名单为空时返回 false。
func (o *OIDC) domainAllowed(email string) bool {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	return slices.Contains(o.Cfg.EmailDomains, strings.ToLower(email[at+1:]))
}

// IsPrivileged 是否持有超级管理员或安全审计员角色。
func IsPrivileged(ctx context.Context, db *ent.Client, uid int) (bool, error) {
	return db.Membership.Query().Where(membership.UserID(uid),
		membership.HasRoleWith(role.KeyIn(rbac.SuperAdmin, rbac.SecurityAuditor))).Exist(ctx)
}

// claimValues claim 值 → 字符串列表（支持字符串或字符串数组），去空白、去重。
func claimValues(v any) []string {
	var out []string
	add := func(x string) {
		x = strings.Trim(strings.TrimSpace(x), "/")
		if x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	switch t := v.(type) {
	case string:
		add(t)
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				add(s)
			}
		}
	}
	return out
}

// deptIndex 组织内部门的 path / code → id 索引。
func deptIndex(ctx context.Context, db *ent.Client, orgID int, match string) (map[string]int, error) {
	ds, err := db.Department.Query().Where(department.OrgID(orgID)).All(ctx)
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	if match == "code" {
		for _, d := range ds {
			if d.ExternalID != "" {
				idx[d.ExternalID] = d.ID
			}
		}
		return idx, nil
	}
	byID := map[int]*ent.Department{}
	for _, d := range ds {
		byID[d.ID] = d
	}
	for _, d := range ds {
		parts := []string{d.Name}
		for p := d.ParentID; p != nil && byID[*p] != nil && len(parts) < 32; p = byID[*p].ParentID {
			parts = append([]string{byID[*p].Name}, parts...)
		}
		idx[strings.Join(parts, "/")] = d.ID
	}
	return idx, nil
}

// oidcSyncDepts 以 IdP 为准同步用户「成员」角色的部门归属（其它角色不动）。
// 全部映射不到时归入默认部门；默认部门也找不到时保持不变。变化时先写审计（fail-closed）再改。
func (s *Service) oidcSyncDepts(c echo.Context, e audit.Entry, u *ent.User, vals []string) error {
	ctx := c.Request().Context()
	cfg := s.OIDC.Cfg
	idx, err := deptIndex(ctx, s.DB, u.OrgID, cfg.DeptMatch)
	if err != nil {
		return err
	}
	var want []int
	var miss []string
	for _, v := range vals {
		if id, ok := idx[v]; ok {
			if !slices.Contains(want, id) {
				want = append(want, id)
			}
		} else {
			miss = append(miss, v)
		}
	}
	usedDefault := false
	if len(want) == 0 {
		id, ok := idx[strings.Trim(strings.TrimSpace(cfg.DeptDefault), "/")]
		if !ok {
			return nil
		}
		want, usedDefault = []int{id}, true
	}
	mr, err := s.DB.Role.Query().Where(role.Key(rbac.Member)).OnlyID(ctx)
	if err != nil {
		return err
	}
	cur, err := s.DB.Membership.Query().Where(membership.UserID(u.ID), membership.RoleID(mr)).All(ctx)
	if err != nil {
		return err
	}
	var have []int
	for _, m := range cur {
		if m.DepartmentID != nil && !slices.Contains(have, *m.DepartmentID) {
			have = append(have, *m.DepartmentID)
		}
	}
	a, b := slices.Clone(have), slices.Clone(want)
	slices.Sort(a)
	slices.Sort(b)
	if slices.Equal(a, b) {
		return nil
	}
	se := e
	se.Action, se.Outcome, se.ActorID, se.OrgID, se.ActorEmail = "user.department.sync", "success", u.ID, u.OrgID, u.Email
	se.ResourceType, se.ResourceID = "user", u.Email
	se.Detail = map[string]any{"via": "oidc", "claim": cfg.DeptClaim, "match": cfg.DeptMatch,
		"from": have, "to": want, "default_used": usedDefault}
	if len(miss) > 0 {
		se.Detail["unmapped"] = miss
	}
	if err := s.Audit.Write(ctx, se); err != nil {
		return audit.ErrRejected
	}
	tx, err := s.DB.Tx(ctx)
	if err != nil {
		return err
	}
	if _, err = tx.Membership.Delete().Where(membership.UserID(u.ID), membership.RoleID(mr)).Exec(ctx); err == nil {
		for i, d := range want {
			if err = tx.Membership.Create().SetUserID(u.ID).SetRoleID(mr).SetDepartmentID(d).SetIsPrimary(i == 0).Exec(ctx); err != nil {
				break
			}
		}
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
