package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	bgt "github.com/SSStq1314/hengdesk/server/internal/budget"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/budget"
	"github.com/SSStq1314/hengdesk/server/internal/ent/department"
	"github.com/SSStq1314/hengdesk/server/internal/ent/membership"
	"github.com/SSStq1314/hengdesk/server/internal/ent/notification"
	"github.com/SSStq1314/hengdesk/server/internal/ent/predicate"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/usage"
)

// ---- 调用前预检查（chat 与模型测试共用） ----

var scopeLabel = map[string]string{"org": "组织", "dept": "部门", "user": "个人", "model": "模型"}

func fmtAmount(metric string, v int64) string {
	if metric == string(budget.MetricCost) {
		return fmt.Sprintf("%.2f 元", float64(v)/1e6)
	}
	return fmt.Sprintf("%d tokens", v)
}

// budgetGate 预检查。超额返回 429 budget_exceeded；Valkey 不可用返回 503 budget_unavailable（fail-closed）。
// 拦截本身写一条 model.call 拒绝审计；审计写不进去同样拒绝（本来就不会调用上游）。
func budgetGate(c echo.Context, bs *bgt.Service, db *ent.Client, modelID int, modelName string) error {
	if bs == nil {
		return nil
	}
	p := auth.Current(c)
	ctx := c.Request().Context()
	ex, err := bs.Precheck(ctx, bgt.Subject{OrgID: p.OrgID, UserID: p.ID, DeptID: usage.PrimaryDept(ctx, db, p.ID), ModelID: modelID})
	if err != nil {
		if !errors.Is(err, bgt.ErrUnavailable) {
			return err
		}
		if aerr := audit.Record(c, audit.Entry{Action: "model.call", Phase: "result", Outcome: "denied", ResourceType: "model",
			ResourceID: modelName, Detail: map[string]any{"reason": bgt.CodeUnavailable}}); aerr != nil {
			return aerr
		}
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"error": &gateway.Error{Code: bgt.CodeUnavailable,
			Message: "预算计数服务暂不可用，为防止超支已暂停模型调用，请稍后再试或联系管理员"}})
	}
	if ex == nil {
		return nil
	}
	if aerr := audit.Record(c, audit.Entry{Action: "model.call", Phase: "result", Outcome: "denied", ResourceType: "model",
		ResourceID: modelName, Detail: map[string]any{"reason": bgt.CodeExceeded, "budget_id": ex.BudgetID, "scope": ex.Scope,
			"used": ex.Used, "limit": ex.Limit}}); aerr != nil {
		return aerr
	}
	reset, _ := time.Parse(time.RFC3339, ex.ResetAt)
	period := "本日"
	if ex.Period == string(budget.PeriodMonth) {
		period = "本月"
	}
	msg := fmt.Sprintf("%s%s预算已用尽（已用 %s / 上限 %s），将于 %s 重置；如需继续使用请联系管理员。",
		period, scopeLabel[ex.Scope], fmtAmount(ex.Metric, ex.Used), fmtAmount(ex.Metric, ex.Limit), reset.Format("2006-01-02 15:04"))
	return c.JSON(http.StatusTooManyRequests, map[string]any{"error": map[string]any{"code": bgt.CodeExceeded, "message": msg, "budget": ex}})
}

// settle usage_record 落库后的预算结算。
func settle(ctx context.Context, bs *bgt.Service, rec *ent.UsageRecord) {
	if bs != nil && rec != nil {
		bs.Settle(context.WithoutCancel(ctx), rec)
	}
}

// ---- 管理接口 /admin/budgets ----

type budgetHandlers struct {
	db *ent.Client
	bs *bgt.Service
	az *authz.Service
}

// scope 当前用户可见的预算范围（D-37 第 9 条）：
//   - 持有 budget.manage（超级管理员、组织管理员）或全组织范围的 budget.read：全部；
//   - 部门管理员（部门范围的 budget.read）：本部门及下级部门的部门预算，以及这些部门成员的个人预算，只读；
//   - 组织 / 模型维度预算不对部门管理员开放。范围外一律 404。
func (h *budgetHandlers) scope(c echo.Context) ([]predicate.Budget, error) {
	p := auth.Current(c)
	if p.Perms.Has(rbac.BudgetManage) {
		return nil, nil
	}
	roots, orgWide := p.Perms.DeptRoots(rbac.BudgetRead)
	if orgWide {
		return nil, nil
	}
	if len(roots) == 0 {
		return nil, authz.Deny(c, "scope:"+string(rbac.BudgetRead))
	}
	ctx := c.Request().Context()
	ids, err := h.az.Subtree(ctx, p.OrgID, roots)
	if err != nil {
		return nil, err
	}
	ids = append(ids, roots...)
	uids, err := h.db.Membership.Query().Where(membership.DepartmentIDIn(ids...), membership.HasUserWith(user.OrgID(p.OrgID))).
		Unique(true).Select(membership.FieldUserID).Ints(ctx)
	if err != nil {
		return nil, err
	}
	return []predicate.Budget{budget.Or(
		budget.And(budget.ScopeEQ(budget.ScopeDept), budget.ScopeIDIn(ids...)),
		budget.And(budget.ScopeEQ(budget.ScopeUser), budget.ScopeIDIn(uids...)),
	)}, nil
}

// get GET /admin/budgets/:id：单条预算与当期用量；范围外 404。
func (h *budgetHandlers) get(c echo.Context) error {
	ps, err := h.scope(c)
	if err != nil {
		return err
	}
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	b, err := h.db.Budget.Query().Where(append(ps, budget.ID(id), budget.OrgID(auth.Current(c).OrgID))...).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return authz.Deny(c, "budget_scope")
		}
		return err
	}
	return c.JSON(http.StatusOK, h.view(ctx, b))
}

// refresh 预算新建或修改后立即以 usage_records 重建当期计数（D-37 第 13 条），并按新的级别重新判断告警（第 11 条）。
// 失败只记日志：预算本身已保存，计数在下次结算或对账时也会重建。
func (h *budgetHandlers) refresh(ctx context.Context, b *ent.Budget) {
	r, err := h.bs.Reconcile(ctx, b)
	if err != nil {
		h.bs.Logger().Warn("budget rebuild after edit failed", "budget", b.ID, "err", err)
		return
	}
	if !b.Enabled {
		return
	}
	if err := h.bs.ResetAlerts(ctx, b, r.DB); err != nil {
		h.bs.Logger().Warn("budget alert re-evaluate failed", "budget", b.ID, "err", err)
	}
}

type budgetView struct {
	ID           int       `json:"id"`
	Scope        string    `json:"scope"`
	ScopeID      int       `json:"scope_id"`
	ScopeName    string    `json:"scope_name"`
	Period       string    `json:"period"`
	Metric       string    `json:"metric"`
	Limit        int64     `json:"limit"`
	ThresholdPct int       `json:"threshold_pct"`
	Action       string    `json:"action"`
	Enabled      bool      `json:"enabled"`
	Note         string    `json:"note"`
	Used         int64     `json:"used"`
	Pct          float64   `json:"pct"`
	PeriodKey    string    `json:"period_key"`
	ResetAt      time.Time `json:"reset_at"`
	// CounterUnavailable Valkey 不可用，Used 为数据库实时汇总。
	CounterUnavailable bool      `json:"counter_unavailable,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func (h *budgetHandlers) view(ctx context.Context, b *ent.Budget) budgetView {
	w := h.bs.Window(b)
	v := budgetView{ID: b.ID, Scope: string(b.Scope), ScopeID: b.ScopeID, ScopeName: h.bs.ScopeName(ctx, b), Period: string(b.Period),
		Metric: string(b.Metric), Limit: b.LimitValue, ThresholdPct: b.ThresholdPct, Action: string(b.Action), Enabled: b.Enabled,
		Note: b.Note, PeriodKey: w.Key, ResetAt: w.End, UpdatedAt: b.UpdatedAt}
	used, err := h.bs.Used(ctx, b)
	if err != nil {
		v.CounterUnavailable = true
		used, _ = h.bs.DBUsed(ctx, b, w)
	}
	v.Used = used
	v.Pct = float64(used) * 100 / float64(b.LimitValue)
	return v
}

func (h *budgetHandlers) list(c echo.Context) error {
	p := auth.Current(c)
	ctx := c.Request().Context()
	ps, err := h.scope(c)
	if err != nil {
		return err
	}
	bs, err := h.db.Budget.Query().Where(append(ps, budget.OrgID(p.OrgID))...).Order(ent.Asc(budget.FieldScope), ent.Asc(budget.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	out := make([]budgetView, 0, len(bs))
	for _, b := range bs {
		out = append(out, h.view(ctx, b))
	}
	return c.JSON(http.StatusOK, out)
}

type budgetReq struct {
	Scope        string `json:"scope"`
	ScopeID      int    `json:"scope_id"`
	Period       string `json:"period"`
	Metric       string `json:"metric"`
	Limit        int64  `json:"limit"`
	ThresholdPct *int   `json:"threshold_pct"`
	Action       string `json:"action"`
	Enabled      *bool  `json:"enabled"`
	Note         string `json:"note"`
}

func (h *budgetHandlers) validate(ctx context.Context, orgID int, r *budgetReq) error {
	if budget.ScopeValidator(budget.Scope(r.Scope)) != nil {
		return bad("维度无效")
	}
	if budget.PeriodValidator(budget.Period(r.Period)) != nil {
		return bad("周期无效")
	}
	if budget.MetricValidator(budget.Metric(r.Metric)) != nil {
		return bad("指标无效")
	}
	if r.Action == "" {
		r.Action = string(budget.ActionBlock)
	}
	if budget.ActionValidator(budget.Action(r.Action)) != nil {
		return bad("超额策略无效")
	}
	if r.Limit <= 0 {
		return bad("上限必须大于 0")
	}
	if r.ThresholdPct == nil {
		d := 80
		r.ThresholdPct = &d
	}
	if *r.ThresholdPct < 1 || *r.ThresholdPct > 100 {
		return bad("告警阈值须在 1–100 之间")
	}
	if len([]rune(r.Note)) > 200 {
		return bad("备注最多 200 字")
	}
	var ok bool
	var err error
	switch budget.Scope(r.Scope) {
	case budget.ScopeOrg:
		r.ScopeID, ok = 0, true
	case budget.ScopeDept:
		ok, err = h.db.Department.Query().Where(department.ID(r.ScopeID), department.OrgID(orgID)).Exist(ctx)
	case budget.ScopeUser:
		ok, err = h.db.User.Query().Where(user.ID(r.ScopeID), user.OrgID(orgID)).Exist(ctx)
	case budget.ScopeModel:
		_, err = h.db.Model.Get(ctx, r.ScopeID)
		ok = err == nil
		if ent.IsNotFound(err) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	if !ok {
		return bad("预算对象不存在")
	}
	return nil
}

func (h *budgetHandlers) auditTx(c echo.Context, tx *ent.Tx, action string, id int, detail map[string]any) error {
	org, actor, email := auth.Actor(c)
	err := (audit.EntWriter{C: tx.Client()}).Write(c.Request().Context(), audit.Entry{OrgID: org, ActorID: actor, ActorEmail: email,
		Action: action, Phase: "result", Outcome: "ok", ResourceType: "budget", ResourceID: strconv.Itoa(id),
		IP: c.RealIP(), UserAgent: c.Request().UserAgent(), RequestID: c.Response().Header().Get(echo.HeaderXRequestID), Detail: detail})
	if err != nil {
		_ = tx.Rollback()
		return audit.ErrRejected
	}
	return nil
}

func reqDetail(r budgetReq) map[string]any {
	d := map[string]any{"scope": r.Scope, "scope_id": r.ScopeID, "period": r.Period, "metric": r.Metric, "limit": r.Limit,
		"threshold_pct": *r.ThresholdPct, "action": r.Action, "note": r.Note}
	if r.Enabled != nil {
		d["enabled"] = *r.Enabled
	}
	return d
}

func dupErr(err error) error {
	if ent.IsConstraintError(err) {
		return echo.NewHTTPError(http.StatusConflict, "同一对象、周期、指标已有预算，请直接修改")
	}
	return err
}

func (h *budgetHandlers) create(c echo.Context) error {
	var r budgetReq
	if err := c.Bind(&r); err != nil {
		return bad("请求格式错误")
	}
	p := auth.Current(c)
	ctx := c.Request().Context()
	if err := h.validate(ctx, p.OrgID, &r); err != nil {
		return err
	}
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	q := tx.Budget.Create().SetOrgID(p.OrgID).SetScope(budget.Scope(r.Scope)).SetScopeID(r.ScopeID).SetPeriod(budget.Period(r.Period)).
		SetMetric(budget.Metric(r.Metric)).SetLimitValue(r.Limit).SetThresholdPct(*r.ThresholdPct).SetAction(budget.Action(r.Action)).
		SetNote(r.Note).SetCreatedBy(p.ID)
	if r.Enabled != nil {
		q.SetEnabled(*r.Enabled)
	}
	b, err := q.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return dupErr(err)
	}
	if err := h.auditTx(c, tx, "budget.create", b.ID, reqDetail(r)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return dupErr(err)
	}
	h.refresh(ctx, b)
	return c.JSON(http.StatusCreated, h.view(ctx, b))
}

func (h *budgetHandlers) load(c echo.Context) (*ent.Budget, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	b, err := h.db.Budget.Query().Where(budget.ID(id), budget.OrgID(auth.Current(c).OrgID)).Only(c.Request().Context())
	if err != nil {
		return nil, notFoundOr(err)
	}
	return b, nil
}

func (h *budgetHandlers) update(c echo.Context) error {
	b, err := h.load(c)
	if err != nil {
		return err
	}
	var r budgetReq
	if err := c.Bind(&r); err != nil {
		return bad("请求格式错误")
	}
	ctx := c.Request().Context()
	if err := h.validate(ctx, b.OrgID, &r); err != nil {
		return err
	}
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	q := tx.Budget.UpdateOneID(b.ID).SetScope(budget.Scope(r.Scope)).SetScopeID(r.ScopeID).SetPeriod(budget.Period(r.Period)).
		SetMetric(budget.Metric(r.Metric)).SetLimitValue(r.Limit).SetThresholdPct(*r.ThresholdPct).SetAction(budget.Action(r.Action)).
		SetNote(r.Note)
	if r.Enabled != nil {
		q.SetEnabled(*r.Enabled)
	}
	nb, err := q.Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return dupErr(err)
	}
	d := reqDetail(r)
	d["before"] = map[string]any{"scope": string(b.Scope), "scope_id": b.ScopeID, "period": string(b.Period), "metric": string(b.Metric),
		"limit": b.LimitValue, "threshold_pct": b.ThresholdPct, "action": string(b.Action), "enabled": b.Enabled}
	if err := h.auditTx(c, tx, "budget.update", b.ID, d); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return dupErr(err)
	}
	h.refresh(ctx, nb)
	return c.JSON(http.StatusOK, h.view(ctx, nb))
}

func (h *budgetHandlers) remove(c echo.Context) error {
	b, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	if err := tx.Budget.DeleteOneID(b.ID).Exec(ctx); err != nil {
		_ = tx.Rollback()
		return notFoundOr(err)
	}
	if err := h.auditTx(c, tx, "budget.delete", b.ID, map[string]any{"scope": string(b.Scope), "scope_id": b.ScopeID,
		"period": string(b.Period), "metric": string(b.Metric), "limit": b.LimitValue}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := h.bs.ForgetAlerts(ctx, b.ID); err != nil {
		h.bs.Logger().Warn("budget alert state cleanup failed", "budget", b.ID, "err", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// reconcile POST /admin/budgets/reconcile：以 usage_records 为准重建全部（或 ?id= 指定）预算的当期计数。
// 先写 attempt 审计（fail-closed），再写结果审计。
func (h *budgetHandlers) reconcile(c echo.Context) error {
	p := auth.Current(c)
	ctx := c.Request().Context()
	q := h.db.Budget.Query().Where(budget.OrgID(p.OrgID))
	target := "all"
	if s := c.QueryParam("id"); s != "" {
		id, err := strconv.Atoi(s)
		if err != nil {
			return authz.ErrNotFound
		}
		q.Where(budget.ID(id))
		target = s
	}
	bs, err := q.All(ctx)
	if err != nil {
		return err
	}
	if target != "all" && len(bs) == 0 {
		return authz.ErrNotFound
	}
	if err := audit.Record(c, audit.Entry{Action: "budget.reconcile", Phase: "attempt", ResourceType: "budget", ResourceID: target}); err != nil {
		return err
	}
	out := make([]bgt.Recon, 0, len(bs))
	for _, b := range bs {
		r, err := h.bs.Reconcile(ctx, b)
		if err != nil {
			_ = audit.Record(c, audit.Entry{Action: "budget.reconcile", Phase: "result", Outcome: "error", ResourceType: "budget",
				ResourceID: target, Detail: map[string]any{"error": err.Error()}})
			if errors.Is(err, bgt.ErrUnavailable) {
				return echo.NewHTTPError(http.StatusServiceUnavailable, "预算计数服务（Valkey）不可用")
			}
			return err
		}
		out = append(out, r)
	}
	drifted := 0
	for _, r := range out {
		if r.Drift != 0 {
			drifted++
		}
	}
	if err := audit.Record(c, audit.Entry{Action: "budget.reconcile", Phase: "result", Outcome: "ok", ResourceType: "budget",
		ResourceID: target, Detail: map[string]any{"budgets": len(out), "drifted": drifted}}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// mine GET /budgets/me：对我生效的预算与当期用量（让被拦截的人知道是哪条预算）。
func (h *budgetHandlers) mine(c echo.Context) error {
	p := auth.Current(c)
	ctx := c.Request().Context()
	sub := bgt.Subject{OrgID: p.OrgID, UserID: p.ID, DeptID: usage.PrimaryDept(ctx, h.db, p.ID), ModelID: -1}
	bs, err := h.bs.Applicable(ctx, sub)
	if err != nil {
		return err
	}
	out := make([]budgetView, 0, len(bs))
	for _, b := range bs {
		out = append(out, h.view(ctx, b))
	}
	return c.JSON(http.StatusOK, out)
}

// ---- 站内提醒 /notifications（只看自己的） ----

type notificationHandlers struct{ db *ent.Client }

func (h *notificationHandlers) list(c echo.Context) error {
	p := auth.Current(c)
	ctx := c.Request().Context()
	q := h.db.Notification.Query().Where(notification.UserID(p.ID))
	unread, err := q.Clone().Where(notification.ReadAtIsNil()).Count(ctx)
	if err != nil {
		return err
	}
	rows, err := q.Order(ent.Desc(notification.FieldID)).Limit(50).All(ctx)
	if err != nil {
		return err
	}
	type item struct {
		ID        int            `json:"id"`
		Kind      string         `json:"kind"`
		Title     string         `json:"title"`
		Detail    map[string]any `json:"detail"`
		Read      bool           `json:"read"`
		CreatedAt time.Time      `json:"created_at"`
	}
	items := make([]item, 0, len(rows))
	for _, r := range rows {
		items = append(items, item{ID: r.ID, Kind: r.Kind, Title: r.Title, Detail: r.Detail, Read: r.ReadAt != nil, CreatedAt: r.CreatedAt})
	}
	return c.JSON(http.StatusOK, map[string]any{"unread": unread, "items": items})
}

func (h *notificationHandlers) markRead(c echo.Context) error {
	p := auth.Current(c)
	if _, err := h.db.Notification.Update().Where(notification.UserID(p.ID), notification.ReadAtIsNil()).
		SetReadAt(time.Now()).Save(c.Request().Context()); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
