package api

import (
	"context"
	"net/http"
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
}

func toRuleView(r *ent.EgressRule) egressRuleView {
	return egressRuleView{ID: r.ID, Kind: string(r.Kind), Value: r.Value, Ports: r.Ports, Note: r.Note, Source: string(r.Source),
		Private: r.Private, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func (h *egressHandlers) listRules(c echo.Context) error {
	rs, err := h.db.EgressRule.Query().Order(ent.Asc(egressrule.FieldKind), ent.Asc(egressrule.FieldValue)).All(c.Request().Context())
	if err != nil {
		return err
	}
	out := make([]egressRuleView, 0, len(rs))
	for _, r := range rs {
		out = append(out, toRuleView(r))
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out, "default_deny": true})
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
		row, err = tx.EgressRule.UpdateOneID(id).SetKind(egressrule.Kind(r.Kind)).SetValue(r.Value).SetPorts(r.PortsString()).
			SetNote(strings.TrimSpace(in.Note)).SetPrivate(r.CoversInternal()).Save(ctx)
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

// logs GET /admin/egress/logs?decision=&host=&module=&user_id=&from=&to=&before_id=&limit=
func (h *egressHandlers) logs(c echo.Context) error {
	ctx := c.Request().Context()
	q := h.db.EgressLog.Query()
	if d := c.QueryParam("decision"); d != "" {
		if egresslog.DecisionValidator(egresslog.Decision(d)) != nil {
			return bad("decision 无效")
		}
		q.Where(egresslog.DecisionEQ(egresslog.Decision(d)))
	}
	if v := strings.ToLower(strings.TrimSpace(c.QueryParam("host"))); v != "" {
		q.Where(egresslog.HostContains(v))
	}
	if v := c.QueryParam("module"); v != "" {
		q.Where(egresslog.Module(v))
	}
	if v := c.QueryParam("reason"); v != "" {
		q.Where(egresslog.Reason(v))
	}
	if v := c.QueryParam("user_id"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return bad("user_id 无效")
		}
		q.Where(egresslog.UserID(n))
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
				return bad(f.k + " 需为 RFC3339 时间")
			}
			f.fn(t)
		}
	}
	if v := c.QueryParam("before_id"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return bad("before_id 无效")
		}
		q.Where(egresslog.IDLT(n))
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
	out := make([]egressLogView, 0, len(rows))
	for _, r := range rows {
		v := egressLogView{ID: r.ID, TS: r.Ts, UserID: r.UserID, ActorEmail: r.ActorEmail, Module: r.Module, Method: r.Method,
			Scheme: r.Scheme, Host: r.Host, Port: r.Port, Path: r.Path, IP: r.IP, Decision: string(r.Decision), Reason: r.Reason,
			Status: r.Status, DurationMS: r.DurationMs, BytesOut: r.BytesOut, BytesIn: r.BytesIn, Error: r.Error}
		if r.Reason != "" {
			v.ReasonText = egress.ReasonText(r.Reason)
		}
		out = append(out, v)
	}
	res := map[string]any{"items": out, "has_more": more}
	if more {
		res["next_before_id"] = rows[len(rows)-1].ID
	}
	return c.JSON(http.StatusOK, res)
}

// providerEgressHint 新建 / 修改模型上游时提示是否已在白名单（只提示，不自动添加）。
func providerEgressHint(ctx context.Context, cl *egress.Client, baseURL string) map[string]any {
	if cl == nil {
		return nil
	}
	ok, why := cl.Check(ctx, baseURL)
	return egressHint(ok, why)
}
