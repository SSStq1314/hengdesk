package api

import (
	"context"
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/agent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/conversation"
	"github.com/SSStq1314/hengdesk/server/internal/ent/department"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/ent/predicate"
	"github.com/SSStq1314/hengdesk/server/internal/ent/usagehourly"
	"github.com/SSStq1314/hengdesk/server/internal/ent/usagerecord"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/usage"
)

// statsHandlers 用量看板（FR-32）：按 budget.read 授权，部门管理员只看本部门子树。
type statsHandlers struct {
	db  *ent.Client
	az  *authz.Service
	loc *time.Location
}

const usageExportMax = 100_000

// usageScope 将过滤条件裁到当前用户可见的部门子树；无权限返回 404。
func (h *statsHandlers) usageScope(c echo.Context, f *usage.Filter) error {
	p := auth.Current(c)
	if p == nil {
		return authz.ErrUnauthenticated
	}
	if p.Perms.Has(rbac.BudgetManage) {
		return nil
	}
	roots, orgWide := p.Perms.DeptRoots(rbac.BudgetRead)
	if orgWide {
		return nil
	}
	if len(roots) == 0 {
		return authz.Deny(c, "scope:"+string(rbac.BudgetRead))
	}
	ids, err := h.az.Subtree(c.Request().Context(), p.OrgID, roots)
	if err != nil {
		return err
	}
	ids = append(ids, roots...)
	if len(f.DeptIDs) == 0 {
		f.DeptIDs = ids
		return nil
	}
	allow := map[int]struct{}{}
	for _, id := range ids {
		allow[id] = struct{}{}
	}
	kept := f.DeptIDs[:0]
	for _, id := range f.DeptIDs {
		if _, ok := allow[id]; ok {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		return authz.Deny(c, "dept_out_of_scope")
	}
	f.DeptIDs = kept
	return nil
}

func parseStatsRange(c echo.Context) (from, to time.Time, err error) {
	parseT := func(k string) (time.Time, error) {
		v := c.QueryParam(k)
		if v == "" {
			return time.Time{}, nil
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, nil
		}
		t, err := time.ParseInLocation("2006-01-02", v, time.Local)
		if err != nil {
			return t, bad(k + " 格式应为 YYYY-MM-DD 或 RFC3339")
		}
		return t, nil
	}
	if from, err = parseT("from"); err != nil {
		return
	}
	if to, err = parseT("to"); err != nil {
		return
	}
	if v := c.QueryParam("to"); len(v) == 10 && !to.IsZero() {
		to = to.AddDate(0, 0, 1)
	}
	if from.IsZero() && to.IsZero() {
		// 默认近 7 天（含今天）
		now := time.Now().In(time.Local)
		to = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1)
		from = to.AddDate(0, 0, -7)
	}
	return
}

// overview GET /admin/stats/overview：指标卡（活跃用户、对话数、token、成本）。
func (h *statsHandlers) overview(c echo.Context) error {
	p := auth.Current(c)
	from, to, err := parseStatsRange(c)
	if err != nil {
		return err
	}
	f := usage.Filter{OrgID: p.OrgID, From: from, To: to}
	if err := h.usageScope(c, &f); err != nil {
		return err
	}
	ctx := c.Request().Context()
	_, tot, err := usage.Summary(ctx, h.db, usage.ByNone, f)
	if err != nil {
		return err
	}
	active, err := h.activeUsers(ctx, f)
	if err != nil {
		return err
	}
	convs, err := h.conversationCount(ctx, f)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{
		"from": from, "to": to,
		"active_users": active, "conversations": convs,
		"totals": tot, "currency": "CNY", "unit": "micros",
	})
}

func (h *statsHandlers) activeUsers(ctx context.Context, f usage.Filter) (int, error) {
	ids, err := h.db.UsageRecord.Query().Where(f.Predicates()...).Unique(true).Select(usagerecord.FieldUserID).Ints(ctx)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (h *statsHandlers) conversationCount(ctx context.Context, f usage.Filter) (int, error) {
	q := h.db.Conversation.Query().Where(conversation.OrgID(f.OrgID), conversation.DeletedAtIsNil())
	if !f.From.IsZero() {
		q = q.Where(conversation.CreatedAtGTE(f.From))
	}
	if !f.To.IsZero() {
		q = q.Where(conversation.CreatedAtLT(f.To))
	}
	if f.UserID != nil {
		q = q.Where(conversation.UserID(*f.UserID))
	}
	if len(f.DeptIDs) > 0 {
		// 对话无 department_id：按主部门落在子树内的用户过滤。
		uids, err := h.db.UsageRecord.Query().Where(usagerecord.OrgID(f.OrgID), usagerecord.DepartmentIDIn(f.DeptIDs...)).
			Unique(true).Select(usagerecord.FieldUserID).Ints(ctx)
		if err != nil {
			return 0, err
		}
		if len(uids) == 0 {
			return 0, nil
		}
		q = q.Where(conversation.UserIDIn(uids...))
	}
	return q.Count(ctx)
}

// timeseries GET /admin/stats/timeseries?grain=hour|day&group_by=total|model|department|agent
func (h *statsHandlers) timeseries(c echo.Context) error {
	p := auth.Current(c)
	from, to, err := parseStatsRange(c)
	if err != nil {
		return err
	}
	f := usage.Filter{OrgID: p.OrgID, From: from, To: to}
	if v := c.QueryParam("model_id"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return bad("model_id 必须是整数")
		}
		f.ModelID = &n
	}
	if v := c.QueryParam("agent_id"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return bad("agent_id 必须是整数")
		}
		f.AgentID = &n
	}
	if v := c.QueryParam("department_id"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return bad("department_id 必须是整数")
		}
		f.DeptIDs = []int{n}
	}
	if err := h.usageScope(c, &f); err != nil {
		return err
	}
	grain := c.QueryParam("grain")
	if grain == "" {
		grain = "day"
	}
	if grain != "hour" && grain != "day" {
		return bad("grain 只能是 hour / day")
	}
	by := usage.GroupBy(c.QueryParam("group_by"))
	if by == "" {
		by = usage.ByNone
	}
	switch by {
	case usage.ByNone, usage.ByModel, usage.ByDept, usage.ByAgent, usage.ByUser:
	default:
		return bad("group_by 只能是 total / model / department / agent / user")
	}
	ctx := c.Request().Context()
	// 优先读小时表；没有数据时回退到 usage_records（首次部署或聚合未跑）。
	points, err := h.seriesFromHourly(ctx, f, grain, by)
	if err != nil {
		return err
	}
	if len(points) == 0 {
		points, err = h.seriesFromRecords(ctx, f, grain, by)
		if err != nil {
			return err
		}
	}
	return c.JSON(http.StatusOK, map[string]any{
		"from": from, "to": to, "grain": grain, "group_by": by, "points": points, "currency": "CNY", "unit": "micros",
	})
}

type seriesPoint struct {
	Bucket string       `json:"bucket"`
	Key    int          `json:"key"`
	Name   string       `json:"name,omitempty"`
	Totals usage.Totals `json:"totals"`
}

func (h *statsHandlers) seriesFromHourly(ctx context.Context, f usage.Filter, grain string, by usage.GroupBy) ([]seriesPoint, error) {
	ps := []predicate.UsageHourly{usagehourly.OrgID(f.OrgID)}
	if !f.From.IsZero() {
		ps = append(ps, usagehourly.BucketAtGTE(f.From.UTC()))
	}
	if !f.To.IsZero() {
		ps = append(ps, usagehourly.BucketAtLT(f.To.UTC()))
	}
	if f.UserID != nil {
		ps = append(ps, usagehourly.UserID(*f.UserID))
	}
	if len(f.DeptIDs) > 0 {
		ps = append(ps, usagehourly.DepartmentIDIn(f.DeptIDs...))
	}
	if f.ModelID != nil {
		ps = append(ps, usagehourly.ModelID(*f.ModelID))
	}
	if f.AgentID != nil {
		ps = append(ps, usagehourly.AgentID(*f.AgentID))
	}
	rows, err := h.db.UsageHourly.Query().Where(ps...).All(ctx)
	if err != nil {
		return nil, err
	}
	type acc struct {
		usage.Totals
		Key int
	}
	idx := map[string]*acc{}
	order := []string{}
	loc := h.loc
	if loc == nil {
		loc = time.Local
	}
	for _, r := range rows {
		b := r.BucketAt.In(loc)
		var bucket string
		if grain == "hour" {
			bucket = b.Format("2006-01-02T15:00:00")
		} else {
			bucket = b.Format("2006-01-02")
		}
		key := 0
		switch by {
		case usage.ByUser:
			key = r.UserID
		case usage.ByDept:
			key = r.DepartmentID
		case usage.ByModel:
			key = r.ModelID
		case usage.ByAgent:
			key = r.AgentID
		}
		k := bucket + "#" + strconv.Itoa(key)
		a, ok := idx[k]
		if !ok {
			a = &acc{Key: key}
			idx[k] = a
			order = append(order, k)
		}
		a.Calls += r.Calls
		a.ErrorCalls += r.ErrorCalls
		a.PromptTokens += r.PromptTokens
		a.CachedTokens += r.CachedTokens
		a.CompletionTokens += r.CompletionTokens
		a.ReasoningTokens += r.ReasoningTokens
		a.TotalTokens += r.TotalTokens
		a.CostMicros += r.CostMicros
		a.EstimatedCalls += r.EstimatedCalls
		a.EstimatedTokens += r.EstimatedTokens
		a.EstimatedCostMicros += r.EstimatedCostMicros
		a.UnknownCalls += r.UnknownCalls
	}
	out := make([]seriesPoint, 0, len(order))
	for _, k := range order {
		a := idx[k]
		bucket, _, _ := splitBucketKey(k)
		out = append(out, seriesPoint{Bucket: bucket, Key: a.Key, Totals: a.Totals})
	}
	h.fillSeriesNames(ctx, f.OrgID, by, out)
	return out, nil
}

func splitBucketKey(k string) (bucket, key string, ok bool) {
	for i := len(k) - 1; i >= 0; i-- {
		if k[i] == '#' {
			return k[:i], k[i+1:], true
		}
	}
	return k, "", false
}

func (h *statsHandlers) seriesFromRecords(ctx context.Context, f usage.Filter, grain string, by usage.GroupBy) ([]seriesPoint, error) {
	rs, err := h.db.UsageRecord.Query().Where(f.Predicates()...).All(ctx)
	if err != nil {
		return nil, err
	}
	type acc struct {
		usage.Totals
		Key int
	}
	idx := map[string]*acc{}
	order := []string{}
	loc := h.loc
	if loc == nil {
		loc = time.Local
	}
	for _, r := range rs {
		b := r.CreatedAt.In(loc)
		var bucket string
		if grain == "hour" {
			bucket = time.Date(b.Year(), b.Month(), b.Day(), b.Hour(), 0, 0, 0, loc).Format("2006-01-02T15:00:00")
		} else {
			bucket = b.Format("2006-01-02")
		}
		key := 0
		switch by {
		case usage.ByUser:
			key = r.UserID
		case usage.ByDept:
			if r.DepartmentID != nil {
				key = *r.DepartmentID
			}
		case usage.ByModel:
			key = r.ModelID
		case usage.ByAgent:
			if r.AgentID != nil {
				key = *r.AgentID
			}
		}
		k := bucket + "#" + strconv.Itoa(key)
		a, ok := idx[k]
		if !ok {
			a = &acc{Key: key}
			idx[k] = a
			order = append(order, k)
		}
		a.Calls++
		if r.Status == usagerecord.StatusError {
			a.ErrorCalls++
		}
		a.PromptTokens += int64(r.PromptTokens)
		a.CachedTokens += int64(r.CachedTokens)
		a.CompletionTokens += int64(r.CompletionTokens)
		a.ReasoningTokens += int64(r.ReasoningTokens)
		a.TotalTokens += int64(r.TotalTokens)
		a.CostMicros += r.CostMicros
		if r.UsageUnknown {
			a.UnknownCalls++
		}
		if r.Estimated {
			a.EstimatedCalls++
			a.EstimatedTokens += int64(r.TotalTokens)
			a.EstimatedCostMicros += r.CostMicros
		}
	}
	out := make([]seriesPoint, 0, len(order))
	for _, k := range order {
		a := idx[k]
		bucket, _, _ := splitBucketKey(k)
		out = append(out, seriesPoint{Bucket: bucket, Key: a.Key, Totals: a.Totals})
	}
	h.fillSeriesNames(ctx, f.OrgID, by, out)
	return out, nil
}

func (h *statsHandlers) fillSeriesNames(ctx context.Context, orgID int, by usage.GroupBy, pts []seriesPoint) {
	ids := map[int]struct{}{}
	for _, p := range pts {
		if p.Key != 0 {
			ids[p.Key] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return
	}
	list := make([]int, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	names := map[int]string{}
	switch by {
	case usage.ByUser:
		us, _ := h.db.User.Query().Where(user.IDIn(list...), user.OrgID(orgID)).All(ctx)
		for _, u := range us {
			names[u.ID] = u.DisplayName
			if names[u.ID] == "" {
				names[u.ID] = u.Email
			}
		}
	case usage.ByDept:
		ds, _ := h.db.Department.Query().Where(department.IDIn(list...), department.OrgID(orgID)).All(ctx)
		for _, d := range ds {
			names[d.ID] = d.Name
		}
	case usage.ByModel:
		ms, _ := h.db.Model.Query().Where(model.IDIn(list...)).All(ctx)
		for _, m := range ms {
			names[m.ID] = m.Name
		}
	case usage.ByAgent:
		as, _ := h.db.Agent.Query().Where(agent.IDIn(list...), agent.OrgID(orgID)).All(ctx)
		for _, a := range as {
			names[a.ID] = a.Name
		}
	}
	for i := range pts {
		pts[i].Name = names[pts[i].Key]
	}
}

// summary GET /admin/stats/summary?group_by=user|department|model|agent|total —— 与 /admin/usage/summary 相同，但按 budget.read 裁剪范围。
func (h *statsHandlers) summary(c echo.Context) error {
	p := auth.Current(c)
	f, err := parseUsageFilter(c, p.OrgID)
	if err != nil {
		return err
	}
	if v := c.QueryParam("agent_id"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return bad("agent_id 必须是整数")
		}
		f.AgentID = &n
	}
	if err := h.usageScope(c, &f); err != nil {
		return err
	}
	by := usage.GroupBy(c.QueryParam("group_by"))
	if by == "" {
		by = usage.ByModel
	}
	ctx := c.Request().Context()
	rows, tot, err := usage.Summary(ctx, h.db, by, f)
	if err != nil {
		if errors.Is(err, usage.ErrBadGroup) {
			return bad("group_by 只能是 user / department / model / agent / total")
		}
		return err
	}
	names := h.statsNames(ctx, p.OrgID, by, rows)
	out := make([]usageRowView, len(rows))
	for i, r := range rows {
		out[i] = usageRowView{Key: r.Key, Name: names[r.Key], Totals: r.Totals}
	}
	return c.JSON(http.StatusOK, map[string]any{"group_by": by, "items": out, "totals": tot, "currency": "CNY", "unit": "micros"})
}

func (h *statsHandlers) statsNames(ctx context.Context, orgID int, by usage.GroupBy, rows []usage.Row) map[int]string {
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		if r.Key != 0 {
			ids = append(ids, r.Key)
		}
	}
	out := map[int]string{}
	if len(ids) == 0 {
		return out
	}
	switch by {
	case usage.ByUser:
		us, _ := h.db.User.Query().Where(user.IDIn(ids...), user.OrgID(orgID)).All(ctx)
		for _, u := range us {
			out[u.ID] = u.DisplayName
			if out[u.ID] == "" {
				out[u.ID] = u.Email
			}
		}
	case usage.ByDept:
		ds, _ := h.db.Department.Query().Where(department.IDIn(ids...), department.OrgID(orgID)).All(ctx)
		for _, d := range ds {
			out[d.ID] = d.Name
		}
	case usage.ByModel:
		ms, _ := h.db.Model.Query().Where(model.IDIn(ids...)).All(ctx)
		for _, m := range ms {
			out[m.ID] = m.Name
		}
	case usage.ByAgent:
		as, _ := h.db.Agent.Query().Where(agent.IDIn(ids...), agent.OrgID(orgID)).All(ctx)
		for _, a := range as {
			out[a.ID] = a.Name
		}
	}
	return out
}

// export GET /admin/stats/export：导出明细 CSV（最多 usageExportMax 行），记审计；审计失败则不导出。
func (h *statsHandlers) export(c echo.Context) error {
	p := auth.Current(c)
	f, err := parseUsageFilter(c, p.OrgID)
	if err != nil {
		return err
	}
	if v := c.QueryParam("agent_id"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return bad("agent_id 必须是整数")
		}
		f.AgentID = &n
	}
	if err := h.usageScope(c, &f); err != nil {
		return err
	}
	ctx := c.Request().Context()
	rs, err := usage.List(ctx, h.db, f, usageExportMax+1)
	if err != nil {
		return err
	}
	truncated := len(rs) > usageExportMax
	if truncated {
		rs = rs[:usageExportMax]
	}
	if err := audit.Record(c, audit.Entry{
		Action: "usage.export", Phase: "result", Outcome: "ok", ResourceType: "usage",
		Detail: map[string]any{"rows": len(rs), "truncated": truncated, "format": "csv",
			"from": f.From, "to": f.To, "user_id": f.UserID, "department_ids": f.DeptIDs, "model_id": f.ModelID, "agent_id": f.AgentID},
	}); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "审计不可用，拒绝导出")
	}
	c.Response().Header().Set(echo.HeaderContentType, "text/csv; charset=utf-8")
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="usage.csv"`)
	c.Response().Header().Set("X-HengDesk-Truncated", strconv.FormatBool(truncated))
	c.Response().WriteHeader(http.StatusOK)
	w := csv.NewWriter(c.Response())
	_ = w.Write([]string{"id", "created_at", "user_id", "department_id", "model_id", "model_name", "agent_id",
		"purpose", "status", "prompt_tokens", "completion_tokens", "total_tokens", "cost_micros", "currency",
		"estimated", "usage_unknown", "request_id"})
	for _, r := range rs {
		dept, agent := "", ""
		if r.DepartmentID != nil {
			dept = strconv.Itoa(*r.DepartmentID)
		}
		if r.AgentID != nil {
			agent = strconv.Itoa(*r.AgentID)
		}
		_ = w.Write([]string{
			strconv.Itoa(r.ID), r.CreatedAt.UTC().Format(time.RFC3339),
			strconv.Itoa(r.UserID), dept, strconv.Itoa(r.ModelID), r.ModelName, agent,
			string(r.Purpose), string(r.Status),
			strconv.Itoa(r.PromptTokens), strconv.Itoa(r.CompletionTokens), strconv.Itoa(r.TotalTokens),
			strconv.FormatInt(r.CostMicros, 10), r.Currency,
			strconv.FormatBool(r.Estimated), strconv.FormatBool(r.UsageUnknown), r.RequestID,
		})
	}
	w.Flush()
	return w.Error()
}
