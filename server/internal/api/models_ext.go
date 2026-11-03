package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/acl"
	"github.com/SSStq1314/hengdesk/server/internal/ent/department"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/ent/modelprovider"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/usage"
)

// ---- 默认模型 ----

// setDefault POST /admin/models/:id/default：设为默认模型（未指定 model 的调用使用它）。
// 在一个事务里清掉其它模型的默认标记，保证最多一个默认。
func (h *modelHandlers) setDefault(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	m, err := h.db.Model.Get(ctx, id)
	if err != nil {
		return notFoundOr(err)
	}
	if !m.Enabled {
		return bad("已停用的模型不能设为默认")
	}
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Model.Update().Where(model.IsDefault(true), model.IDNEQ(id)).SetIsDefault(false).Save(ctx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if m, err = tx.Model.UpdateOneID(id).SetIsDefault(true).Save(ctx); err != nil {
		_ = tx.Rollback()
		return notFoundOr(err)
	}
	// 审计与变更同事务：审计写不进去则回滚（fail-closed）。
	org, actor, email := auth.Actor(c)
	if err := (audit.EntWriter{C: tx.Client()}).Write(ctx, audit.Entry{OrgID: org, ActorID: actor, ActorEmail: email,
		Action: "model.default.set", Phase: "result", Outcome: "ok", ResourceType: "model", ResourceID: strconv.Itoa(id),
		IP: c.RealIP(), UserAgent: c.Request().UserAgent(), RequestID: c.Response().Header().Get(echo.HeaderXRequestID),
		Detail: map[string]any{"name": m.Name}}); err != nil {
		_ = tx.Rollback()
		return audit.ErrRejected
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toModelView(m))
}

// ---- 模型 ACL ----

type aclView struct {
	SubjectType     string `json:"subject_type"`
	SubjectID       int    `json:"subject_id"`
	SubjectName     string `json:"subject_name"`
	IncludeChildren bool   `json:"include_children"`
	Perms           int    `json:"perms"`
}

func (h *modelHandlers) modelOr404(c echo.Context) (*ent.Model, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	m, err := h.db.Model.Get(c.Request().Context(), id)
	if err != nil {
		return nil, notFoundOr(err)
	}
	return m, nil
}

// listACL GET /admin/models/:id/acl：谁能用这个模型。
func (h *modelHandlers) listACL(c echo.Context) error {
	m, err := h.modelOr404(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	p := auth.Current(c)
	rows, err := h.db.ACL.Query().Where(acl.OrgID(p.OrgID), acl.ResourceType(rbac.ResModel), acl.ResourceID(strconv.Itoa(m.ID))).
		Order(ent.Asc(acl.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	out := make([]aclView, 0, len(rows))
	for _, r := range rows {
		out = append(out, aclView{SubjectType: string(r.SubjectType), SubjectID: r.SubjectID,
			SubjectName:     h.subjectName(ctx, p.OrgID, string(r.SubjectType), r.SubjectID),
			IncludeChildren: r.IncludeChildren, Perms: r.Perms})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out, "manage_perm_bypass": true})
}

// subjectName 主体显示名；主体不存在（或不在本组织）返回空串。
func (h *modelHandlers) subjectName(ctx context.Context, orgID int, typ string, id int) string {
	switch typ {
	case "user":
		if u, err := h.db.User.Query().Where(user.ID(id), user.OrgID(orgID)).Only(ctx); err == nil {
			if u.DisplayName != "" {
				return u.DisplayName + " <" + u.Email + ">"
			}
			return u.Email
		}
	case "dept":
		if d, err := h.db.Department.Query().Where(department.ID(id), department.OrgID(orgID)).Only(ctx); err == nil {
			return d.Name
		}
	case "role":
		if r, err := h.db.Role.Query().Where(role.ID(id)).Only(ctx); err == nil {
			return r.Name
		}
	}
	return ""
}

type aclInput struct {
	SubjectType     string `json:"subject_type"`
	SubjectID       int    `json:"subject_id"`
	IncludeChildren bool   `json:"include_children"`
}

// grantACL POST /admin/models/:id/acl：授予某主体使用权（use）。
func (h *modelHandlers) grantACL(c echo.Context) error {
	m, err := h.modelOr404(c)
	if err != nil {
		return err
	}
	var in aclInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	return h.setACL(c, m, in.SubjectType, in.SubjectID, in.IncludeChildren, rbac.BitUse, "model.acl.grant")
}

// revokeACL DELETE /admin/models/:id/acl/:type/:sid。
func (h *modelHandlers) revokeACL(c echo.Context) error {
	m, err := h.modelOr404(c)
	if err != nil {
		return err
	}
	sid, err := strconv.Atoi(c.Param("sid"))
	if err != nil {
		return authz.ErrNotFound
	}
	return h.setACL(c, m, c.Param("type"), sid, false, 0, "model.acl.revoke")
}

func (h *modelHandlers) setACL(c echo.Context, m *ent.Model, typ string, sid int, children bool, bits int, action string) error {
	ctx := c.Request().Context()
	p := auth.Current(c)
	if typ != "user" && typ != "dept" && typ != "role" {
		return bad("subject_type 只能是 user / dept / role")
	}
	name := h.subjectName(ctx, p.OrgID, typ, sid)
	if name == "" && bits != 0 {
		return bad("授权对象不存在")
	}
	az := &authz.Service{DB: h.db}
	if err := az.Grant(ctx, p.OrgID, p.ID, rbac.ResModel, strconv.Itoa(m.ID),
		authz.Subject{Type: typ, ID: sid, IncludeChildren: children}, bits); err != nil {
		if errors.Is(err, authz.ErrBadSubject) {
			return bad("授权对象不合法")
		}
		return err
	}
	if err := h.record(c, action, "model", m.ID, map[string]any{"name": m.Name, "subject_type": typ,
		"subject_id": sid, "subject_name": name, "include_children": children && typ == "dept"}); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- 测试连接 ----

type testResult struct {
	OK        bool           `json:"ok"`
	LatencyMS int64          `json:"latency_ms"`
	Models    []string       `json:"models,omitempty"`
	Reply     string         `json:"reply,omitempty"`
	Usage     *gateway.Usage `json:"usage,omitempty"`
	Error     *gateway.Error `json:"error,omitempty"`
}

// testProvider POST /admin/model-providers/:id/test：GET {base_url}/models，验证地址与 Key。
func (h *modelHandlers) testProvider(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	p, err := h.db.ModelProvider.Query().Where(modelprovider.ID(id)).Only(ctx)
	if err != nil {
		return notFoundOr(err)
	}
	res := testResult{}
	start := time.Now()
	up, ge := h.gw.Upstream(p)
	if ge == nil {
		res.Models, ge = h.gw.Client.ListModels(ctx, *up)
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	res.OK, res.Error = ge == nil, ge
	if len(res.Models) > 50 {
		res.Models = res.Models[:50]
	}
	if err := h.recordTest(c, "model.provider.test", "model_provider", id, p.Name, res); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, res)
}

// testModel POST /admin/models/:id/test：发一条极短的对话（max_tokens 8），记 usage（purpose=test）。
func (h *modelHandlers) testModel(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	m, err := h.db.Model.Query().Where(model.ID(id)).WithProvider().Only(ctx)
	if err != nil {
		return notFoundOr(err)
	}
	res := testResult{}
	start := time.Now()
	tgt, ge := h.gw.Target(m, false)
	var out *gateway.ChatResponse
	if ge == nil {
		req := &gateway.ChatRequest{Messages: []gateway.Message{{Role: "user", Content: []byte(`"ping"`)}}, MaxTokens: 8}
		cfg := tgt.Config
		// 测试连接只验证连通性：思考模式下 8 个 token 不够输出，统一关掉思考。
		cfg.Thinking = "off"
		out, ge = h.gw.Client.Chat(ctx, req, tgt.Upstream, cfg)
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	res.OK, res.Error = ge == nil, ge
	if out != nil {
		u := out.Usage
		res.Usage = &u
		var s string
		if len(out.Message.Content) > 0 {
			_ = json.Unmarshal(out.Message.Content, &s)
		}
		if len([]rune(s)) > 200 {
			s = string([]rune(s)[:200])
		}
		res.Reply = s
	}
	if tgt != nil && (out != nil || (ge != nil && ge.UpstreamStatus != 0)) {
		p := auth.Current(c)
		rec := usage.Record{OrgID: p.OrgID, UserID: p.ID, DeptID: usage.PrimaryDept(ctx, h.db, p.ID), Model: m,
			ProviderID: m.ProviderID, RequestID: c.Response().Header().Get(echo.HeaderXRequestID), Purpose: "test",
			Latency: time.Since(start)}
		if out != nil {
			rec.Usage = out.Usage
		} else {
			rec.ErrorCode = ge.Code
		}
		if _, err := usage.Write(context.WithoutCancel(ctx), h.db, rec); err != nil {
			c.Logger().Errorf("usage record write failed: %v", err)
		}
	}
	if err := h.recordTest(c, "model.test", "model", id, m.Name, res); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, res)
}

func (h *modelHandlers) recordTest(c echo.Context, action, resType string, id int, name string, r testResult) error {
	d := map[string]any{"name": name, "ok": r.OK, "latency_ms": r.LatencyMS}
	if r.Error != nil {
		d["error_code"], d["error"] = r.Error.Code, r.Error.Message
	}
	outcome := "ok"
	if !r.OK {
		outcome = "error"
	}
	return audit.Record(c, audit.Entry{Action: action, Phase: "result", Outcome: outcome, ResourceType: resType,
		ResourceID: strconv.Itoa(id), Detail: d})
}

// ---- 用量统计 ----

func parseUsageFilter(c echo.Context, orgID int) (usage.Filter, error) {
	f := usage.Filter{OrgID: orgID}
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
	var err error
	if f.From, err = parseT("from"); err != nil {
		return f, err
	}
	if f.To, err = parseT("to"); err != nil {
		return f, err
	}
	if v := c.QueryParam("to"); len(v) == 10 && !f.To.IsZero() {
		f.To = f.To.AddDate(0, 0, 1) // 按日期给的 to 含当天
	}
	intp := func(k string) (*int, error) {
		v := c.QueryParam(k)
		if v == "" {
			return nil, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, bad(k + " 必须是整数")
		}
		return &n, nil
	}
	if f.UserID, err = intp("user_id"); err != nil {
		return f, err
	}
	if f.ModelID, err = intp("model_id"); err != nil {
		return f, err
	}
	d, err := intp("department_id")
	if err != nil {
		return f, err
	}
	if d != nil {
		f.DeptIDs = []int{*d}
	}
	switch v := c.QueryParam("purpose"); v {
	case "", "chat", "test":
		f.Purpose = v
	default:
		return f, bad("purpose 只能是 chat / test")
	}
	return f, nil
}

type usageRowView struct {
	Key  int    `json:"key"`
	Name string `json:"name"`
	usage.Totals
}

// usageSummary GET /admin/usage/summary?group_by=user|department|model|total&from=&to=
func (h *modelHandlers) usageSummary(c echo.Context) error {
	p := auth.Current(c)
	f, err := parseUsageFilter(c, p.OrgID)
	if err != nil {
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
			return bad("group_by 只能是 user / department / model / total")
		}
		return err
	}
	names := h.names(ctx, p.OrgID, by, rows)
	out := make([]usageRowView, len(rows))
	for i, r := range rows {
		out[i] = usageRowView{Key: r.Key, Name: names[r.Key], Totals: r.Totals}
	}
	return c.JSON(http.StatusOK, map[string]any{"group_by": by, "items": out, "totals": tot, "currency": "CNY",
		"unit": "micros"})
}

func (h *modelHandlers) names(ctx context.Context, orgID int, by usage.GroupBy, rows []usage.Row) map[int]string {
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
			out[u.ID] = u.Email
			if u.DisplayName != "" {
				out[u.ID] = u.DisplayName
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
	}
	return out
}

// usageRecords GET /admin/usage/records：明细（最近 200 条，可用同样的过滤条件）。
func (h *modelHandlers) usageRecords(c echo.Context) error {
	p := auth.Current(c)
	f, err := parseUsageFilter(c, p.OrgID)
	if err != nil {
		return err
	}
	rs, err := usage.List(c.Request().Context(), h.db, f, 200)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"items": rs})
}

// ---- 普通用户 ----

type myModelView struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
}

// myModels GET /api/v1/models：当前用户可调用的模型（已启用且有 use 权限；model.manage 可见全部）。
func (h *modelHandlers) myModels(c echo.Context, az *authz.Service) error {
	p := auth.Current(c)
	if p == nil {
		return authz.ErrUnauthenticated
	}
	ctx := c.Request().Context()
	q := h.db.Model.Query().Where(model.Enabled(true), model.HasProviderWith(modelprovider.Enabled(true)))
	if !p.Perms.Has(rbac.ModelManage) {
		ids, err := az.VisibleIDs(ctx, p, rbac.ResModel, rbac.BitUse)
		if err != nil {
			return err
		}
		ns := make([]int, 0, len(ids))
		for _, s := range ids {
			if n, err := strconv.Atoi(s); err == nil {
				ns = append(ns, n)
			}
		}
		q = q.Where(model.IDIn(ns...))
	}
	ms, err := q.Order(ent.Asc(model.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	out := make([]myModelView, len(ms))
	for i, m := range ms {
		out[i] = myModelView{ID: m.ID, Name: m.Name, IsDefault: m.IsDefault}
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

// myUsage GET /api/v1/usage/me：本人用量汇总（按模型），from/to 同管理接口。
func (h *modelHandlers) myUsage(c echo.Context) error {
	p := auth.Current(c)
	if p == nil {
		return authz.ErrUnauthenticated
	}
	f, err := parseUsageFilter(c, p.OrgID)
	if err != nil {
		return err
	}
	uid := p.ID
	f.UserID, f.DeptIDs = &uid, nil
	ctx := c.Request().Context()
	rows, tot, err := usage.Summary(ctx, h.db, usage.ByModel, f)
	if err != nil {
		return err
	}
	names := h.names(ctx, p.OrgID, usage.ByModel, rows)
	out := make([]usageRowView, len(rows))
	for i, r := range rows {
		out[i] = usageRowView{Key: r.Key, Name: names[r.Key], Totals: r.Totals}
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out, "totals": tot})
}
