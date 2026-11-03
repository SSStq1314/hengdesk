package budget

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/budget"
	"github.com/SSStq1314/hengdesk/server/internal/ent/deptclosure"
	"github.com/SSStq1314/hengdesk/server/internal/ent/membership"
	"github.com/SSStq1314/hengdesk/server/internal/ent/notification"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

const (
	// KindAlert 站内提醒类型。
	KindAlert = "budget_alert"
	// AuditAlert 告警审计动作。
	AuditAlert = "budget.alert"
)

func managerRoles() []string {
	var out []string
	for _, r := range rbac.Builtin {
		for _, p := range r.Perms {
			if p == rbac.BudgetManage && !r.DeptScoped {
				out = append(out, r.Key)
			}
		}
	}
	return out
}

// Recipients 告警收件人：
//   - 所有预算管理员（持有 budget.manage 的角色）；
//   - 部门预算：该部门及其上级部门的部门管理员（AC-10）；
//   - 个人预算：本人，以及其主部门（含上级）的部门管理员。
func (s *Service) Recipients(ctx context.Context, b *ent.Budget) ([]int, error) {
	seen := map[int]bool{}
	var out []int
	add := func(ids []int) {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	active := membership.HasUserWith(user.OrgID(b.OrgID), user.StatusEQ(user.StatusActive))
	ids, err := s.DB.Membership.Query().Where(active, membership.HasRoleWith(role.KeyIn(managerRoles()...))).
		Select(membership.FieldUserID).Ints(ctx)
	if err != nil {
		return nil, err
	}
	add(ids)
	var dept *int
	switch b.Scope {
	case budget.ScopeDept:
		d := b.ScopeID
		dept = &d
	case budget.ScopeUser:
		u, err := s.DB.User.Get(ctx, b.ScopeID)
		if err == nil && u.OrgID == b.OrgID && u.Status == user.StatusActive {
			add([]int{u.ID})
		}
		ms, err := s.DB.Membership.Query().Where(membership.UserID(b.ScopeID), membership.DepartmentIDNotNil()).
			Order(ent.Desc(membership.FieldIsPrimary), ent.Asc(membership.FieldID)).Limit(1).All(ctx)
		if err != nil {
			return nil, err
		}
		if len(ms) > 0 {
			dept = ms[0].DepartmentID
		}
	}
	if dept != nil {
		anc, err := s.DB.DeptClosure.Query().Where(deptclosure.OrgID(b.OrgID), deptclosure.DescendantID(*dept)).
			Unique(true).Select(deptclosure.FieldAncestorID).Ints(ctx)
		if err != nil {
			return nil, err
		}
		anc = append(anc, *dept)
		ids, err := s.DB.Membership.Query().Where(active, membership.HasRoleWith(role.Key(rbac.DeptAdmin)),
			membership.DepartmentIDIn(anc...)).Select(membership.FieldUserID).Ints(ctx)
		if err != nil {
			return nil, err
		}
		add(ids)
	}
	return out, nil
}

func dedupeKey(b *ent.Budget, w Window, level int) string {
	return fmt.Sprintf("budget:%d:%d:%s:%d", b.ID, b.UpdatedAt.UnixMilli(), w.Key, level)
}

// alert 同一预算、同一周期、同一级别只发一次：先用 Valkey SETNX 挡掉重复，再在一个事务里写审计与站内提醒
// （收件人 + dedupe_key 唯一索引兜底）。审计写不进去则整个提醒不发，并清掉 Valkey 标记等下一次结算重试。
func (s *Service) alert(ctx context.Context, b *ent.Budget, w Window, level int, used int64) error {
	key := dedupeKey(b, w, level)
	gate := "budget:alerted:" + key
	ok, err := s.R.SetNX(ctx, gate, 1, w.End.Sub(s.now())+24*time.Hour).Result()
	if err != nil || !ok {
		return err
	}
	if err := s.sendAlert(ctx, b, w, level, used, key); err != nil {
		s.R.Del(context.WithoutCancel(ctx), gate)
		return err
	}
	return nil
}

func (s *Service) sendAlert(ctx context.Context, b *ent.Budget, w Window, level int, used int64, key string) error {
	if n, err := s.DB.Notification.Query().Where(notification.DedupeKey(key)).Count(ctx); err != nil || n > 0 {
		return err
	}
	to, err := s.Recipients(ctx, b)
	if err != nil {
		return err
	}
	detail := map[string]any{"budget_id": b.ID, "scope": string(b.Scope), "scope_id": b.ScopeID, "period": string(b.Period),
		"period_key": w.Key, "metric": string(b.Metric), "limit": b.LimitValue, "used": used, "level": level,
		"threshold_pct": b.ThresholdPct, "action": string(b.Action), "reset_at": w.End.Format(time.RFC3339), "scope_name": s.ScopeName(ctx, b)}
	if used > b.LimitValue {
		detail["overage"] = used - b.LimitValue
	}
	title := fmt.Sprintf("预算已用 %d%%", level)
	if level >= 100 {
		title = "预算已用尽"
	}
	tx, err := s.DB.Tx(ctx)
	if err != nil {
		return err
	}
	ad := map[string]any{"recipients": len(to)}
	for k, v := range detail {
		ad[k] = v
	}
	if err := (audit.EntWriter{C: tx.Client()}).Write(ctx, audit.Entry{OrgID: b.OrgID, ActorEmail: "system", Action: AuditAlert,
		Phase: "result", Outcome: "success", ResourceType: "budget", ResourceID: itoa(b.ID), Detail: ad}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if len(to) > 0 {
		bulk := make([]*ent.NotificationCreate, 0, len(to))
		for _, uid := range to {
			bulk = append(bulk, tx.Notification.Create().SetOrgID(b.OrgID).SetUserID(uid).SetKind(KindAlert).
				SetTitle(title).SetDetail(detail).SetDedupeKey(key))
		}
		if err := tx.Notification.CreateBulk(bulk...).Exec(ctx); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ScopeName 预算对象的可读名称。
func (s *Service) ScopeName(ctx context.Context, b *ent.Budget) string {
	switch b.Scope {
	case budget.ScopeDept:
		if d, err := s.DB.Department.Get(ctx, b.ScopeID); err == nil {
			return d.Name
		}
	case budget.ScopeUser:
		if u, err := s.DB.User.Get(ctx, b.ScopeID); err == nil {
			if u.DisplayName != "" {
				return u.DisplayName + "（" + u.Email + "）"
			}
			return u.Email
		}
	case budget.ScopeModel:
		if m, err := s.DB.Model.Get(ctx, b.ScopeID); err == nil {
			return m.Name
		}
	case budget.ScopeOrg:
		return "全组织"
	}
	return "#" + strconv.Itoa(b.ScopeID)
}
