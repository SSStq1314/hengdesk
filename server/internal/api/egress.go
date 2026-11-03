package api

import (
	"context"
	"encoding/csv"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/egress"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/egresslog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/egressrule"
)

// 出站控制（FR-27 / AC-09）：白名单管理（egress.manage）与出站日志查询（egress.log.read，只读）。
type egressHandlers struct {
	db     *ent.Client
	client *egress.Client
	policy *egress.DBPolicy
}

type egressRuleView struct {
	ID        int       `json:"id"`
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	Ports     string    `json:"ports"`
	Note      string    `json:"note"`
	Source    string    `json:"source"`
	Private   bool      `json:"private"`
	CreatedBy int       `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// AnyPort 端口为 "*"（任意端口），管理端显示风险提示。
	AnyPort bool `json:"any_port"`
	// NeedsReview 升级 / OIDC 自动添加、尚未复核（D-38 第 1 条）；或 D-39 迁移收窄过端口的手工条目。
	NeedsReview bool       `json:"needs_review"`
	ReviewedAt  *time.Time `json:"reviewed_at,omitempty"`
	ReviewedBy  int        `json:"reviewed_by,omitempty"`
}

func toRuleView(r *ent.EgressRule) egressRuleView {
	return egressRuleView{ID: r.ID, Kind: string(r.Kind), Value: r.Value, Ports: r.Ports, Note: r.Note, Source: string(r.Source),
		Private: r.Private, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		AnyPort: strings.TrimSpace(r.Ports) == "*", NeedsReview: (r.Source != egressrule.SourceManual || r.ReviewRequired) && r.ReviewedAt == nil,
		ReviewedAt: r.ReviewedAt, ReviewedBy: r.ReviewedBy}
}

func (h *egressHandlers) listRules(c echo.Context) error {
	rs, err := h.db.EgressRule.Query().Order(ent.Asc(egressrule.FieldKind), ent.Asc(egressrule.FieldValue)).All(c.Request().Context())
	if err != nil {
		return err
	}
	out := make([]egressRuleView, 0, len(rs))
	pending := 0
	for _, r := range rs {
		v := toRuleView(r)
		if v.NeedsReview {
			pending++
		}
		out = append(out, v)
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out, "default_deny": true, "default_port": egress.DefaultPort, "pending_review": pending})
}

type egressRuleReq struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Ports string `json:"ports"`
	Note  string `json:"note"`
}

func (in *egressRuleReq) parse() (egress.Rule, error) {
	r, err := egress.ParseRule(in.Kind, in.Value, in.Ports)
	if err != nil {
		return r, bad(err.Error())
	}
	if len([]rune(in.Note)) > 200 {
		return r, bad("备注最多 200 字")
	}
	return r, nil
}

// writeAudit 与变更同事务；审计写不进去则回滚（fail-closed）。
func egressAudit(c echo.Context, tx *ent.Tx, action string, id int, detail map[string]any) error {
	org, actor, email := auth.Actor(c)
	return (audit.EntWriter{C: tx.Client()}).Write(c.Request().Context(), audit.Entry{OrgID: org, ActorID: actor, ActorEmail: email,
		Action: action, Phase: "result", Outcome: "ok", ResourceType: "egress_rule", ResourceID: strconv.Itoa(id),
		IP: c.RealIP(), UserAgent: c.Request().UserAgent(), RequestID: c.Response().Header().Get(echo.HeaderXRequestID), Detail: detail})
}

func ruleDetail(r egress.Rule, note string) map[string]any {
	return map[string]any{"kind": r.Kind, "value": r.Value, "ports": r.PortsString(), "note": note, "private": r.CoversInternal()}
}

func (h *egressHandlers) save(c echo.Context, id int) error {
	var in egressRuleReq
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	r, err := in.parse()
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	var old *ent.EgressRule
	if id != 0 {
		if old, err = h.db.EgressRule.Get(ctx, id); err != nil {
			return notFoundOr(err)
		}
	}
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	var row *ent.EgressRule
	if id == 0 {
		row, err = tx.EgressRule.Create().SetKind(egressrule.Kind(r.Kind)).SetValue(r.Value).SetPorts(r.PortsString()).
			SetNote(strings.TrimSpace(in.Note)).SetPrivate(r.CoversInternal()).SetCreatedBy(auth.Current(c).ID).Save(ctx)
	} else {
		// 管理员修改过自动添加的条目，视为已复核。
		row, err = tx.EgressRule.UpdateOneID(id).SetKind(egressrule.Kind(r.Kind)).SetValue(r.Value).SetPorts(r.PortsString()).
			SetNote(strings.TrimSpace(in.Note)).SetPrivate(r.CoversInternal()).
			SetReviewedAt(time.Now()).SetReviewedBy(auth.Current(c).ID).Save(ctx)
	}
	if err != nil {
		_ = tx.Rollback()
		if ent.IsConstraintError(err) {
			return echo.NewHTTPError(http.StatusConflict, "相同的白名单条目已存在")
		}
		return notFoundOr(err)
	}
	action, detail := "egress.rule.create", ruleDetail(r, row.Note)
	if old != nil {
		action = "egress.rule.update"
		detail["before"] = map[string]any{"kind": string(old.Kind), "value": old.Value, "ports": old.Ports, "private": old.Private}
	}
	if err := egressAudit(c, tx, action, row.ID, detail); err != nil {
		_ = tx.Rollback()
		return audit.ErrRejected
	}
	// 放行内网 / 环回网段属于高风险操作，单独再记一条，便于审计员检索。
	if r.CoversInternal() && (old == nil || !old.Private || old.Value != r.Value || old.Kind != egressrule.Kind(r.Kind)) {
		if err := egressAudit(c, tx, "egress.private_allow", row.ID, detail); err != nil {
			_ = tx.Rollback()
			return audit.ErrRejected
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	h.policy.Invalidate()
	code := http.StatusOK
	if id == 0 {
		code = http.StatusCreated
	}
	return c.JSON(code, toRuleView(row))
}

func (h *egressHandlers) createRule(c echo.Context) error { return h.save(c, 0) }

// reviewRule POST /admin/egress/rules/:id/review：管理员确认一条升级 / OIDC 自动添加的条目（D-38 第 1 条）。
func (h *egressHandlers) reviewRule(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	old, err := h.db.EgressRule.Get(ctx, id)
	if err != nil {
		return notFoundOr(err)
	}
	if old.Source == egressrule.SourceManual && !old.ReviewRequired {
		return bad("手工添加的条目不需要复核")
	}
	if old.ReviewedAt != nil {
		return c.JSON(http.StatusOK, toRuleView(old))
	}
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	row, err := tx.EgressRule.UpdateOneID(id).SetReviewedAt(time.Now()).SetReviewedBy(auth.Current(c).ID).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return notFoundOr(err)
	}
	if err := egressAudit(c, tx, "egress.rule.review", id, map[string]any{"kind": string(row.Kind), "value": row.Value,
		"ports": row.Ports, "source": string(row.Source), "private": row.Private}); err != nil {
		_ = tx.Rollback()
		return audit.ErrRejected
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toRuleView(row))
}

func (h *egressHandlers) updateRule(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	return h.save(c, id)
}

func (h *egressHandlers) deleteRule(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	old, err := h.db.EgressRule.Get(ctx, id)
	if err != nil {
		return notFoundOr(err)
	}
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	if err := tx.EgressRule.DeleteOneID(id).Exec(ctx); err != nil {
		_ = tx.Rollback()
		return notFoundOr(err)
	}
	if err := egressAudit(c, tx, "egress.rule.delete", id, map[string]any{"kind": string(old.Kind), "value": old.Value,
		"ports": old.Ports, "private": old.Private, "source": string(old.Source)}); err != nil {
		_ = tx.Rollback()
		return audit.ErrRejected
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	h.policy.Invalidate()
	return c.NoContent(http.StatusNoContent)
}

// check POST /admin/egress/check {"url": "..."}：不发请求，判断当前白名单是否放行。
func (h *egressHandlers) check(c echo.Context) error {
	var in struct {
		URL string `json:"url"`
	}
	if err := c.Bind(&in); err != nil || in.URL == "" {
		return bad("请提供 url")
	}
	ok, why := h.client.Check(c.Request().Context(), in.URL)
	return c.JSON(http.StatusOK, egressHint(ok, why))
}

func egressHint(ok bool, why string) map[string]any {
	out := map[string]any{"allowed": ok}
	if !ok {
		out["reason"], out["message"] = why, egress.ReasonText(why)
	}
	return out
}

type egressLogView struct {
	ID         int       `json:"id"`
	TS         time.Time `json:"ts"`
	UserID     *int      `json:"user_id,omitempty"`
	ActorEmail string    `json:"actor_email"`
	Module     string    `json:"module"`
	Method     string    `json:"method"`
	Scheme     string    `json:"scheme"`
	Host       string    `json:"host"`
	Port       int       `json:"port"`
	Path       string    `json:"path"`
	IP         string    `json:"ip"`
	Decision   string    `json:"decision"`
	Reason     string    `json:"reason"`
	ReasonText string    `json:"reason_text,omitempty"`
	Status     int       `json:"status"`
	DurationMS int64     `json:"duration_ms"`
	BytesOut   int64     `json:"bytes_out"`
	BytesIn    int64     `json:"bytes_in"`
	Error      string    `json:"error"`
}

// logQuery 解析出站日志的筛选条件；filters 是规范化后的条件，写入敏感读取审计。
func (h *egressHandlers) logQuery(c echo.Context) (*ent.EgressLogQuery, map[string]any, error) {
	q := h.db.EgressLog.Query()
	filters := map[string]any{}
	if d := c.QueryParam("decision"); d != "" {
		if egresslog.DecisionValidator(egresslog.Decision(d)) != nil {
			return nil, nil, bad("decision 无效")
		}
		q.Where(egresslog.DecisionEQ(egresslog.Decision(d)))
		filters["decision"] = d
	}
	if v := strings.ToLower(strings.TrimSpace(c.QueryParam("host"))); v != "" {
		q.Where(egresslog.HostContains(v))
		filters["host"] = v
	}
	if v := c.QueryParam("module"); v != "" {
		q.Where(egresslog.Module(v))
		filters["module"] = v
	}
	if v := c.QueryParam("reason"); v != "" {
		q.Where(egresslog.Reason(v))
		filters["reason"] = v
	}
	if v := c.QueryParam("user_id"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, nil, bad("user_id 无效")
		}
		q.Where(egresslog.UserID(n))
		filters["user_id"] = n
	}
	for _, f := range []struct {
		k  string
		fn func(time.Time)
	}{
		{"from", func(t time.Time) { q.Where(egresslog.TsGTE(t)) }},
		{"to", func(t time.Time) { q.Where(egresslog.TsLT(t)) }},
	} {
		if v := c.QueryParam(f.k); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return nil, nil, bad(f.k + " 需为 RFC3339 时间")
			}
			f.fn(t)
			filters[f.k] = t.UTC().Format(time.RFC3339)
		}
	}
	return q, filters, nil
}

// logReadAudit 出站日志的查看 / 筛选 / 导出按敏感读取记审计（D-38 第 10 条，与 D-30 一致）：
// 操作者、时间由审计自动补齐，另记筛选条件与返回 / 导出行数；写不进去就拒绝（不返回数据）。
func logReadAudit(c echo.Context, action string, filters map[string]any, rows int, extra map[string]any) error {
	d := map[string]any{"filters": filters, "rows": rows}
	for k, v := range extra {
		d[k] = v
	}
	return audit.Record(c, audit.Entry{Action: action, Phase: "result", Outcome: "ok", ResourceType: "egress_logs", ResourceID: "query", Detail: d})
}

func toLogView(r *ent.EgressLog) egressLogView {
	v := egressLogView{ID: r.ID, TS: r.Ts, UserID: r.UserID, ActorEmail: r.ActorEmail, Module: r.Module, Method: r.Method,
		Scheme: r.Scheme, Host: r.Host, Port: r.Port, Path: r.Path, IP: r.IP, Decision: string(r.Decision), Reason: r.Reason,
		Status: r.Status, DurationMS: r.DurationMs, BytesOut: r.BytesOut, BytesIn: r.BytesIn, Error: r.Error}
	if r.Reason != "" {
		v.ReasonText = egress.ReasonText(r.Reason)
	}
	return v
}

// logs GET /admin/egress/logs?decision=&host=&module=&reason=&user_id=&from=&to=&before_id=&limit=
func (h *egressHandlers) logs(c echo.Context) error {
	ctx := c.Request().Context()
	q, filters, err := h.logQuery(c)
	if err != nil {
		return err
	}
	beforeID := 0
	if v := c.QueryParam("before_id"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return bad("before_id 无效")
		}
		q.Where(egresslog.IDLT(n))
		beforeID = n
	}
	limit := 100
	if v, err := strconv.Atoi(c.QueryParam("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	rows, err := q.Order(ent.Desc(egresslog.FieldID)).Limit(limit + 1).All(ctx)
	if err != nil {
		return err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	action := "egress.log.view"
	if len(filters) > 0 {
		action = "egress.log.filter"
	}
	if err := logReadAudit(c, action, filters, len(rows), map[string]any{"before_id": beforeID, "limit": limit}); err != nil {
		return err
	}
	out := make([]egressLogView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toLogView(r))
	}
	res := map[string]any{"items": out, "has_more": more}
	if more {
		res["next_before_id"] = rows[len(rows)-1].ID
	}
	return c.JSON(http.StatusOK, res)
}

// egressExportMax 单次导出上限（D-39 第 2 条：10 万行，分批查询流式输出，不一次读入内存）；超过请缩小时间范围。
var egressExportMax = 100000

// egressExportBatch 每批查询的行数。
const egressExportBatch = 1000

// exportLogs GET /admin/egress/logs/export：按当前筛选条件导出 CSV（最多 egressExportMax 行）。
// 先确定导出范围（不超过当前最大 ID 的快照）与行数并写好审计，再分批查询、流式输出；
// 审计写不进去返回 503，不输出任何数据。
func (h *egressHandlers) exportLogs(c echo.Context) error {
	ctx := c.Request().Context()
	q, filters, err := h.logQuery(c)
	if err != nil {
		return err
	}
	top, err := q.Clone().Order(ent.Desc(egresslog.FieldID)).Limit(1).IDs(ctx)
	if err != nil {
		return err
	}
	total := 0
	if len(top) > 0 {
		if total, err = q.Clone().Where(egresslog.IDLTE(top[0])).Count(ctx); err != nil {
			return err
		}
	}
	rows := min(total, egressExportMax)
	truncated := total > egressExportMax
	if err := logReadAudit(c, "egress.log.export", filters, rows, map[string]any{"truncated": truncated, "format": "csv", "matched": total}); err != nil {
		return err
	}
	c.Response().Header().Set(echo.HeaderContentType, "text/csv; charset=utf-8")
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="egress-logs.csv"`)
	if truncated {
		c.Response().Header().Set("X-Export-Truncated", "true")
	}
	c.Response().WriteHeader(http.StatusOK)
	_, _ = c.Response().Write([]byte("\xEF\xBB\xBF"))
	w := csv.NewWriter(c.Response())
	_ = w.Write([]string{"id", "ts", "user_id", "actor_email", "module", "method", "scheme", "host", "port", "path", "ip",
		"decision", "reason", "status", "duration_ms", "bytes_out", "bytes_in", "error"})
	written := 0
	cursor := 0
	if len(top) > 0 {
		cursor = top[0] + 1
	}
	for written < rows {
		batch, err := q.Clone().Where(egresslog.IDLT(cursor)).Order(ent.Desc(egresslog.FieldID)).Limit(min(egressExportBatch, rows-written)).All(ctx)
		if err != nil {
			return err // 响应已开始输出，只能中断；审计已记录本次导出
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			uid := ""
			if r.UserID != nil {
				uid = strconv.Itoa(*r.UserID)
			}
			_ = w.Write([]string{strconv.Itoa(r.ID), r.Ts.Format(time.RFC3339), uid, csvSafe(r.ActorEmail), csvSafe(r.Module),
				r.Method, r.Scheme, csvSafe(r.Host), strconv.Itoa(r.Port), csvSafe(r.Path), r.IP, string(r.Decision), r.Reason,
				strconv.Itoa(r.Status), strconv.FormatInt(r.DurationMs, 10), strconv.FormatInt(r.BytesOut, 10), strconv.FormatInt(r.BytesIn, 10), csvSafe(r.Error)})
		}
		written += len(batch)
		cursor = batch[len(batch)-1].ID
		w.Flush()
		if err := w.Error(); err != nil {
			return nil // 客户端断开
		}
	}
	w.Flush()
	return w.Error()
}

// blocked GET /admin/egress/blocked?days=7：最近被拦截的出站请求，按 主机 + 端口 + 原因 聚合（D-39 第 1 条），
// 便于管理员发现迁移收窄端口后被拦的正常请求并补加白名单。按敏感读取记审计，写不进去不返回数据。
func (h *egressHandlers) blocked(c echo.Context) error {
	days := 7
	if v, err := strconv.Atoi(c.QueryParam("days")); err == nil && v > 0 && v <= 90 {
		days = v
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	var groups []struct {
		Host   string `json:"host"`
		Port   int    `json:"port"`
		Reason string `json:"reason"`
		Count  int    `json:"count"`
		LastID int    `json:"last_id"`
	}
	err := h.db.EgressLog.Query().Where(egresslog.DecisionEQ(egresslog.DecisionDeny), egresslog.TsGTE(since)).
		GroupBy(egresslog.FieldHost, egresslog.FieldPort, egresslog.FieldReason).
		Aggregate(ent.As(ent.Count(), "count"), ent.As(ent.Max(egresslog.FieldID), "last_id")).
		Scan(c.Request().Context(), &groups)
	if err != nil {
		return err
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Count != groups[j].Count {
			return groups[i].Count > groups[j].Count
		}
		return groups[i].LastID > groups[j].LastID
	})
	if len(groups) > 200 {
		groups = groups[:200]
	}
	ids := make([]int, len(groups))
	for i, g := range groups {
		ids[i] = g.LastID
	}
	lasts, err := h.db.EgressLog.Query().Where(egresslog.IDIn(ids...)).Select(egresslog.FieldID, egresslog.FieldTs, egresslog.FieldModule).All(c.Request().Context())
	if err != nil {
		return err
	}
	last := map[int]*ent.EgressLog{}
	for _, l := range lasts {
		last[l.ID] = l
	}
	if err := logReadAudit(c, "egress.log.blocked", map[string]any{"decision": "deny", "days": days}, len(groups), nil); err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		out = append(out, map[string]any{"host": g.Host, "port": g.Port, "reason": g.Reason, "reason_text": egress.ReasonText(g.Reason),
			"count": g.Count, "last_ts": last[g.LastID].Ts, "last_module": last[g.LastID].Module})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out, "days": days})
}

// providerEgressHint 新建 / 修改模型上游时提示是否已在白名单（只提示，不自动添加）。
func providerEgressHint(ctx context.Context, cl *egress.Client, baseURL string) map[string]any {
	if cl == nil {
		return nil
	}
	ok, why := cl.Check(ctx, baseURL)
	return egressHint(ok, why)
}
