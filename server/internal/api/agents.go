package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/agent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/agentversion"
	"github.com/SSStq1314/hengdesk/server/internal/ent/schema"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// 智能体（M1 第六块，FR-20）。访问控制全部走 acl（resource_type=agent）：
//   - view：可在广场看到、查看名称 / 描述 / 开场白；use：可发起对话；edit：可改草稿、调试、看版本；manage：发布、删除、授权。
//   - 未发布的智能体只对拥有 edit 的人可见；其他人（含有 view/use 的人）一律 404（§9.5 边界条件）。
//   - 越权、不存在、已删除一律 404，未登录 401；所有写操作先写 attempt 审计，写失败拒绝执行。

const (
	agentMaxSteps     = 10
	agentDefaultSteps = maxToolSteps
	agentPromptMax    = 8000
	agentOpeningMax   = 1000
)

type agentHandlers struct {
	db  *ent.Client
	az  *authz.Service
	cvh *convHandlers
	kbh *kbHandlers
	log *slog.Logger
	now func() time.Time
}

// agentPerms 计算 p 对智能体的权限位（manage 蕴含 edit/use/view，授权时已归一化）。
func (h *agentHandlers) agentPerms(ctx context.Context, p *auth.Principal, ids []int) (map[int]int, error) {
	out := map[int]int{}
	for _, bit := range []int{rbac.BitView, rbac.BitUse, rbac.BitEdit, rbac.BitManage} {
		vis, err := h.az.VisibleIDs(ctx, p, rbac.ResAgent, bit)
		if err != nil {
			return nil, err
		}
		for _, s := range vis {
			id, err := strconv.Atoi(s)
			if err != nil || (ids != nil && !slices.Contains(ids, id)) {
				continue
			}
			out[id] |= bit
		}
	}
	return out, nil
}

// visible 未发布只对 edit 可见；已发布对 view 可见。
func agentVisible(a *ent.Agent, bits int) bool {
	if bits&rbac.BitEdit != 0 {
		return true
	}
	return a.PublishedVersion > 0 && bits&rbac.BitView != 0
}

// agentOr404 取本人可见且拥有 bit 的智能体；否则 404（不暴露存在性）。
func (h *agentHandlers) agentOr404(c echo.Context, bit int) (*auth.Principal, *ent.Agent, int, error) {
	p, err := principal(c)
	if err != nil {
		return nil, nil, 0, err
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return nil, nil, 0, authz.Deny(c, "agent:bad_id")
	}
	ctx := c.Request().Context()
	a, err := h.db.Agent.Query().Where(agent.ID(id), agent.OrgID(p.OrgID), agent.DeletedAtIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil, 0, authz.Deny(c, "agent:missing")
	}
	if err != nil {
		return nil, nil, 0, err
	}
	perms, err := h.agentPerms(ctx, p, []int{id})
	if err != nil {
		return nil, nil, 0, err
	}
	bits := perms[id]
	if !agentVisible(a, bits) || bits&bit == 0 {
		return nil, nil, 0, authz.Deny(c, "agent:acl")
	}
	return p, a, bits, nil
}

type agentView struct {
	ID               int       `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	Opening          string    `json:"opening"`
	PublishedVersion int       `json:"published_version"`
	Published        bool      `json:"published"`
	DraftDirty       bool      `json:"draft_dirty,omitempty"`
	Perms            []string  `json:"perms"`
	CreatedBy        int       `json:"created_by,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	// 以下草稿配置只返回给拥有 edit 的人（系统提示词等属于内部配置）。
	Draft *schema.AgentConfig `json:"draft,omitempty"`
}

func agentDraft(a *ent.Agent) schema.AgentConfig {
	kbs, tools := a.KBIds, a.ToolIds
	if kbs == nil {
		kbs = []int{}
	}
	if tools == nil {
		tools = []int{}
	}
	return schema.AgentConfig{Name: a.Name, Description: a.Description, SystemPrompt: a.SystemPrompt, Opening: a.Opening,
		ModelID: a.ModelID, KBIDs: kbs, ToolIDs: tools, MaxSteps: a.MaxSteps}
}

// published 取最新发布快照（未发布返回 nil）。
func (h *agentHandlers) published(ctx context.Context, a *ent.Agent) (*schema.AgentConfig, error) {
	if a.PublishedVersion == 0 {
		return nil, nil
	}
	v, err := h.db.AgentVersion.Query().Where(agentversion.AgentID(a.ID), agentversion.Version(a.PublishedVersion)).Only(ctx)
	if err != nil {
		return nil, err
	}
	cfg := v.Config
	return &cfg, nil
}

// view 对外展示：未拥有 edit 的人看到的是发布版本的名称 / 描述 / 开场白。
func (h *agentHandlers) view(ctx context.Context, a *ent.Agent, bits int) (agentView, error) {
	v := agentView{ID: a.ID, Name: a.Name, Description: a.Description, Opening: a.Opening, PublishedVersion: a.PublishedVersion,
		Published: a.PublishedVersion > 0, Perms: permNames(bits), CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
	if bits&rbac.BitEdit != 0 {
		d := agentDraft(a)
		v.Draft, v.DraftDirty, v.CreatedBy = &d, a.DraftDirty, a.CreatedBy
		return v, nil
	}
	pub, err := h.published(ctx, a)
	if err != nil {
		return v, err
	}
	if pub != nil {
		v.Name, v.Description, v.Opening = pub.Name, pub.Description, pub.Opening
	}
	return v, nil
}

// list GET /agents?scope=manage|use
//   - 默认（广场）：本人有 use 的已发布智能体；
//   - scope=manage（管理后台）：本人有 edit 的智能体（含未发布）。
func (h *agentHandlers) list(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	perms, err := h.agentPerms(ctx, p, nil)
	if err != nil {
		return err
	}
	manage := c.QueryParam("scope") == "manage"
	ids := []int{}
	for id, b := range perms {
		if (manage && b&rbac.BitEdit != 0) || (!manage && b&rbac.BitUse != 0) {
			ids = append(ids, id)
		}
	}
	out := []agentView{}
	if len(ids) > 0 {
		q := h.db.Agent.Query().Where(agent.OrgID(p.OrgID), agent.IDIn(ids...), agent.DeletedAtIsNil())
		if !manage {
			q = q.Where(agent.PublishedVersionGT(0))
		}
		as, err := q.Order(ent.Desc(agent.FieldUpdatedAt), ent.Desc(agent.FieldID)).All(ctx)
		if err != nil {
			return err
		}
		for _, a := range as {
			b := perms[a.ID]
			if !manage {
				b &^= rbac.BitEdit | rbac.BitManage // 广场只展示发布版本
			}
			v, err := h.view(ctx, a, b)
			if err != nil {
				return err
			}
			v.Perms = permNames(perms[a.ID])
			out = append(out, v)
		}
	}
	roots, orgWide := p.Perms.DeptRoots(rbac.AgentCreate)
	return c.JSON(http.StatusOK, map[string]any{"items": out, "can_create": orgWide || len(roots) > 0})
}

type agentInput struct {
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	SystemPrompt *string `json:"system_prompt"`
	Opening      *string `json:"opening"`
	ModelID      *int    `json:"model_id"`
	ClearModel   bool    `json:"clear_model"`
	KBIDs        *[]int  `json:"kb_ids"`
	ToolIDs      *[]int  `json:"tool_ids"`
	MaxSteps     *int    `json:"max_steps"`
}

// apply 把输入合并到配置上并校验。绑定的模型 / 知识库 / 工具必须是操作者本人当前可用的（否则 404，不暴露存在性）。
func (h *agentHandlers) apply(c echo.Context, p *auth.Principal, cfg *schema.AgentConfig, in agentInput) error {
	if in.Name != nil {
		cfg.Name = strings.TrimSpace(*in.Name)
	}
	if cfg.Name == "" || len([]rune(cfg.Name)) > 100 {
		return bad("名称必填，最多 100 字")
	}
	if in.Description != nil {
		cfg.Description = strings.TrimSpace(*in.Description)
	}
	if len([]rune(cfg.Description)) > 500 {
		return bad("描述最多 500 字")
	}
	if in.SystemPrompt != nil {
		cfg.SystemPrompt = strings.TrimSpace(*in.SystemPrompt)
	}
	if len([]rune(cfg.SystemPrompt)) > agentPromptMax {
		return bad("系统提示词最多 " + strconv.Itoa(agentPromptMax) + " 字")
	}
	if in.Opening != nil {
		cfg.Opening = strings.TrimSpace(*in.Opening)
	}
	if len([]rune(cfg.Opening)) > agentOpeningMax {
		return bad("开场白最多 " + strconv.Itoa(agentOpeningMax) + " 字")
	}
	if in.MaxSteps != nil {
		cfg.MaxSteps = *in.MaxSteps
	}
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = agentDefaultSteps
	}
	if cfg.MaxSteps < 1 || cfg.MaxSteps > agentMaxSteps {
		return bad("最大工具步数需在 1–" + strconv.Itoa(agentMaxSteps) + " 之间")
	}
	ctx := c.Request().Context()
	if in.ClearModel {
		cfg.ModelID = nil
	} else if in.ModelID != nil {
		m, err := h.db.Model.Get(ctx, *in.ModelID)
		if ent.IsNotFound(err) {
			return authz.Deny(c, "model:not_found")
		}
		if err != nil {
			return err
		}
		if _, err := h.cvh.ch.allowed(c, p, m.Name); err != nil {
			return err
		}
		id := m.ID
		cfg.ModelID = &id
	}
	if in.KBIDs != nil {
		ids, err := h.cvh.checkKBs(c, p, *in.KBIDs)
		if err != nil {
			return err
		}
		cfg.KBIDs = ids
	}
	if in.ToolIDs != nil {
		ids, err := h.cvh.checkTools(c, p, *in.ToolIDs)
		if err != nil {
			return err
		}
		cfg.ToolIDs = ids
	}
	if cfg.KBIDs == nil {
		cfg.KBIDs = []int{}
	}
	if cfg.ToolIDs == nil {
		cfg.ToolIDs = []int{}
	}
	return nil
}

func agentDetail(a *ent.Agent, cfg *schema.AgentConfig) map[string]any {
	d := map[string]any{}
	if a != nil {
		d["agent_id"], d["name"] = a.ID, a.Name
	}
	if cfg != nil {
		d["name"], d["model_id"], d["kb_ids"], d["tool_ids"], d["max_steps"] = cfg.Name, cfg.ModelID, cfg.KBIDs, cfg.ToolIDs, cfg.MaxSteps
	}
	return d
}

func (h *agentHandlers) record(c echo.Context, action, phase, outcome string, id int, detail map[string]any) error {
	rid := ""
	if id > 0 {
		rid = strconv.Itoa(id)
	}
	return audit.Record(c, audit.Entry{Action: action, Phase: phase, Outcome: outcome, ResourceType: rbac.ResAgent, ResourceID: rid, Detail: detail})
}

// create POST /agents（需要 agent.create）：创建者获得 manage；部门管理员创建时默认共享给其管辖部门（发布后可见）。
func (h *agentHandlers) create(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in agentInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	cfg := schema.AgentConfig{}
	if err := h.apply(c, p, &cfg, in); err != nil {
		return err
	}
	detail := agentDetail(nil, &cfg)
	if err := h.record(c, "agent.create", "attempt", "", 0, detail); err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := tx.Agent.Create().SetOrgID(p.OrgID).SetName(cfg.Name).SetDescription(cfg.Description).SetSystemPrompt(cfg.SystemPrompt).
		SetOpening(cfg.Opening).SetNillableModelID(cfg.ModelID).SetKBIds(cfg.KBIDs).SetToolIds(cfg.ToolIDs).SetMaxSteps(cfg.MaxSteps).
		SetCreatedBy(p.ID).Save(ctx)
	if err != nil {
		return err
	}
	if err := (&authz.Service{DB: tx.Client()}).InitShare(ctx, p, rbac.ResAgent, strconv.Itoa(a.ID)); err != nil {
		if errors.Is(err, authz.ErrOutsideSubtree) {
			return authz.Deny(c, "agent:create_scope")
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	detail["agent_id"] = a.ID
	if err := h.record(c, "agent.create", "result", "ok", a.ID, detail); err != nil {
		h.log.Error("agent create result audit failed", "agent", a.ID)
	}
	v, err := h.view(ctx, a, rbac.Normalize(rbac.BitManage))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, v)
}

func (h *agentHandlers) get(c echo.Context) error {
	_, a, bits, err := h.agentOr404(c, rbac.BitView)
	if err != nil {
		return err
	}
	v, err := h.view(c.Request().Context(), a, bits)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, v)
}

// update PATCH /agents/:id（edit）：只改草稿，线上仍用已发布版本，直到再次发布。
func (h *agentHandlers) update(c echo.Context) error {
	p, a, bits, err := h.agentOr404(c, rbac.BitEdit)
	if err != nil {
		return err
	}
	var in agentInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	cfg := agentDraft(a)
	if err := h.apply(c, p, &cfg, in); err != nil {
		return err
	}
	if err := h.record(c, "agent.update", "attempt", "", a.ID, agentDetail(a, &cfg)); err != nil {
		return err
	}
	na, err := h.saveDraft(c.Request().Context(), a, cfg)
	if err != nil {
		return notFoundOr(err)
	}
	v, err := h.view(c.Request().Context(), na, bits)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, v)
}

func (h *agentHandlers) saveDraft(ctx context.Context, a *ent.Agent, cfg schema.AgentConfig) (*ent.Agent, error) {
	u := a.Update().SetName(cfg.Name).SetDescription(cfg.Description).SetSystemPrompt(cfg.SystemPrompt).SetOpening(cfg.Opening).
		SetKBIds(cfg.KBIDs).SetToolIds(cfg.ToolIDs).SetMaxSteps(cfg.MaxSteps).SetDraftDirty(true)
	if cfg.ModelID == nil {
		u.ClearModelID()
	} else {
		u.SetModelID(*cfg.ModelID)
	}
	return u.Save(ctx)
}

// remove DELETE /agents/:id（manage）：软删除；已有会话不再能继续对话。
func (h *agentHandlers) remove(c echo.Context) error {
	_, a, _, err := h.agentOr404(c, rbac.BitManage)
	if err != nil {
		return err
	}
	if err := h.record(c, "agent.delete", "attempt", "", a.ID, agentDetail(a, nil)); err != nil {
		return err
	}
	if _, err := a.Update().SetDeletedAt(h.now()).Save(c.Request().Context()); err != nil {
		return notFoundOr(err)
	}
	return c.NoContent(http.StatusNoContent)
}

// publish POST /agents/:id/publish {"note"}（manage）：把当前草稿存为新版本并上线。
func (h *agentHandlers) publish(c echo.Context) error {
	p, a, bits, err := h.agentOr404(c, rbac.BitManage)
	if err != nil {
		return err
	}
	var in struct {
		Note string `json:"note"`
	}
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	note := strings.TrimSpace(in.Note)
	if len([]rune(note)) > 200 {
		return bad("发布说明最多 200 字")
	}
	cfg := agentDraft(a)
	ver := a.PublishedVersion + 1
	ctx := c.Request().Context()
	if last, err := h.db.AgentVersion.Query().Where(agentversion.AgentID(a.ID)).Aggregate(ent.Max(agentversion.FieldVersion)).Int(ctx); err == nil && last >= ver {
		ver = last + 1
	}
	d := agentDetail(a, &cfg)
	d["version"], d["note"] = ver, note
	if err := h.record(c, "agent.publish", "attempt", "", a.ID, d); err != nil {
		return err
	}
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.AgentVersion.Create().SetOrgID(p.OrgID).SetAgentID(a.ID).SetVersion(ver).SetConfig(cfg).SetNote(note).
		SetPublishedBy(p.ID).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return fileErr(http.StatusConflict, "有其他人同时在发布，请刷新后重试")
		}
		return err
	}
	na, err := tx.Agent.UpdateOne(a).SetPublishedVersion(ver).SetDraftDirty(false).Save(ctx)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	v, err := h.view(ctx, na, bits)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, v)
}

// versions GET /agents/:id/versions（edit）。
func (h *agentHandlers) versions(c echo.Context) error {
	_, a, _, err := h.agentOr404(c, rbac.BitEdit)
	if err != nil {
		return err
	}
	vs, err := h.db.AgentVersion.Query().Where(agentversion.AgentID(a.ID)).Order(ent.Desc(agentversion.FieldVersion)).
		Limit(100).All(c.Request().Context())
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(vs))
	for _, v := range vs {
		out = append(out, map[string]any{"version": v.Version, "note": v.Note, "published_by": v.PublishedBy,
			"created_at": v.CreatedAt, "current": v.Version == a.PublishedVersion, "config": v.Config})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

// restore POST /agents/:id/versions/:v/restore（edit）：把历史版本恢复为草稿（不自动发布）。
// 恢复时同样按操作者本人校验绑定资源，本人已不可用的资源会被拒绝（404），避免借历史版本越权绑定。
func (h *agentHandlers) restore(c echo.Context) error {
	p, a, bits, err := h.agentOr404(c, rbac.BitEdit)
	if err != nil {
		return err
	}
	ver, err := strconv.Atoi(c.Param("v"))
	if err != nil || ver <= 0 {
		return authz.Deny(c, "agent_version:bad_id")
	}
	ctx := c.Request().Context()
	v, err := h.db.AgentVersion.Query().Where(agentversion.AgentID(a.ID), agentversion.Version(ver)).Only(ctx)
	if ent.IsNotFound(err) {
		return authz.Deny(c, "agent_version:missing")
	}
	if err != nil {
		return err
	}
	src := v.Config
	cfg := schema.AgentConfig{}
	in := agentInput{Name: &src.Name, Description: &src.Description, SystemPrompt: &src.SystemPrompt, Opening: &src.Opening,
		ModelID: src.ModelID, ClearModel: src.ModelID == nil, KBIDs: &src.KBIDs, ToolIDs: &src.ToolIDs, MaxSteps: &src.MaxSteps}
	if err := h.apply(c, p, &cfg, in); err != nil {
		return err
	}
	d := agentDetail(a, &cfg)
	d["from_version"] = ver
	if err := h.record(c, "agent.restore", "attempt", "", a.ID, d); err != nil {
		return err
	}
	na, err := h.saveDraft(ctx, a, cfg)
	if err != nil {
		return notFoundOr(err)
	}
	out, err := h.view(ctx, na, bits)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

func (h *agentHandlers) listACL(c echo.Context) error {
	p, a, _, err := h.agentOr404(c, rbac.BitManage)
	if err != nil {
		return err
	}
	return h.kbh.listRows(c, p.OrgID, rbac.ResAgent, strconv.Itoa(a.ID))
}

func (h *agentHandlers) grantACL(c echo.Context) error {
	p, a, _, err := h.agentOr404(c, rbac.BitManage)
	if err != nil {
		return err
	}
	var in kbACLInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	bits, ok := rbac.BitNames[in.Perm]
	if !ok {
		return bad("perm 只能是 view / use / edit / manage")
	}
	return h.kbh.grant(c, p, rbac.ResAgent, strconv.Itoa(a.ID), in, bits, "agent.acl.grant", agentDetail(a, nil))
}

func (h *agentHandlers) revokeACL(c echo.Context) error {
	p, a, _, err := h.agentOr404(c, rbac.BitManage)
	if err != nil {
		return err
	}
	sid, err := strconv.Atoi(c.Param("sid"))
	if err != nil {
		return authz.ErrNotFound
	}
	in := kbACLInput{SubjectType: c.Param("type"), SubjectID: sid}
	return h.kbh.grant(c, p, rbac.ResAgent, strconv.Itoa(a.ID), in, 0, "agent.acl.revoke", agentDetail(a, nil))
}

func mountAgents(g *echo.Group, d Deps, az *authz.Service, cvh *convHandlers) *agentHandlers {
	lg := d.Log
	if lg == nil {
		lg = slog.Default()
	}
	h := &agentHandlers{db: d.DB, az: az, cvh: cvh, kbh: &kbHandlers{db: d.DB, az: az, auth: d.Auth, log: lg}, log: lg, now: time.Now}
	g.GET("/agents", h.list)
	g.POST("/agents", h.create, authz.Require(rbac.AgentCreate))
	g.GET("/agents/:id", h.get)
	g.PATCH("/agents/:id", h.update)
	g.DELETE("/agents/:id", h.remove)
	g.POST("/agents/:id/publish", h.publish)
	g.GET("/agents/:id/versions", h.versions)
	g.POST("/agents/:id/versions/:v/restore", h.restore)
	g.GET("/agents/:id/acl", h.listACL)
	g.POST("/agents/:id/acl", h.grantACL)
	g.DELETE("/agents/:id/acl/:type/:sid", h.revokeACL)
	g.GET("/agents/:id/runs", h.listRuns)
	g.GET("/agents/:id/runs/:rid", h.getRun)
	return h
}
