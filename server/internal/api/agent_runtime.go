package api

// 智能体运行时（M1 第六块 PR2，FR-20/21）。
//
// 会话可绑定一个智能体：正式会话使用最新发布版本；调试会话（agent_debug）使用草稿，需 edit。
// 生成时：系统提示词 → 知识库检索 → 工具循环（复用第五块）。调用者权限 = 员工权限 ∩ 智能体授权：
// 知识库 / 工具按员工当前权限过滤，不可用的静默剔除（调试会话在 done 事件里带回 dropped）。
// 每条回复写一条 agent_runs；审批续跑更新同一行。审计 fail-closed，越权 404。

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
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/agent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/agentrun"
	"github.com/SSStq1314/hengdesk/server/internal/ent/knowledgebase"
	"github.com/SSStq1314/hengdesk/server/internal/ent/schema"
	"github.com/SSStq1314/hengdesk/server/internal/ent/tool"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/kb"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// agentSession 本轮生成使用的智能体配置（已按调用者权限过滤）。
type agentSession struct {
	AgentID  int
	Version  int // 发布版本号；调试为 0
	Debug    bool
	Name     string
	Opening  string
	Prompt   string
	ModelID  *int
	KBIDs    []int
	ToolIDs  []int
	MaxSteps int
	Dropped  []schema.AgentDropped
	RunID    int
}

// errAgentReadonly 智能体停用或软删除后，已有会话改为只读（D-45 第 8 条）。
var errAgentReadonly = errors.New("agent_readonly")

func agentReadonlyHTTP() *echo.HTTPError {
	return echo.NewHTTPError(http.StatusConflict, "智能体已停用或已删除，会话只读，不能继续对话").SetInternal(errAgentReadonly)
}

// bindAgent 校验并返回可绑定的智能体配置。
// existing=true 表示已有会话（停用/删除 → 409 只读）；false 表示新建/改绑（停用/删除 → 404）。
func (h *convHandlers) bindAgent(c echo.Context, p *auth.Principal, id int, debug, existing bool) (*ent.Agent, *schema.AgentConfig, int, error) {
	if id <= 0 {
		return nil, nil, 0, authz.Deny(c, "agent:bad_id")
	}
	ctx := c.Request().Context()
	// 含已删除行：已有会话需区分「不存在」与「只读」。
	a, err := h.db.Agent.Query().Where(agent.ID(id), agent.OrgID(p.OrgID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil, 0, authz.Deny(c, "agent:missing")
	}
	if err != nil {
		return nil, nil, 0, err
	}
	if a.DeletedAt != nil || a.Disabled {
		if existing {
			return nil, nil, 0, agentReadonlyHTTP()
		}
		return nil, nil, 0, authz.Deny(c, "agent:disabled")
	}
	ah := &agentHandlers{db: h.db, az: h.az}
	perms, err := ah.agentPerms(ctx, p, []int{id})
	if err != nil {
		return nil, nil, 0, err
	}
	bits := perms[id]
	if debug {
		if bits&rbac.BitEdit == 0 {
			return nil, nil, 0, authz.Deny(c, "agent:debug")
		}
		cfg := agentDraft(a)
		return a, &cfg, 0, nil
	}
	if !agentVisible(a, bits) || bits&rbac.BitUse == 0 || a.PublishedVersion == 0 {
		return nil, nil, 0, authz.Deny(c, "agent:use")
	}
	cfg, err := ah.published(ctx, a)
	if err != nil {
		return nil, nil, 0, err
	}
	if cfg == nil {
		return nil, nil, 0, authz.Deny(c, "agent:unpublished")
	}
	return a, cfg, a.PublishedVersion, nil
}

// resolveAgent 按会话绑定加载配置，并按调用者当前权限静默剔除不可用的知识库 / 工具（D-45 第 4 条：调用人权限 ∩ 绑定范围）。
func (h *convHandlers) resolveAgent(c echo.Context, p *auth.Principal, cv *ent.Conversation) (*agentSession, error) {
	if cv.AgentID == nil {
		return nil, nil
	}
	a, cfg, ver, err := h.bindAgent(c, p, *cv.AgentID, cv.AgentDebug, true)
	if err != nil {
		return nil, err
	}
	sess := &agentSession{
		AgentID: a.ID, Version: ver, Debug: cv.AgentDebug, Name: cfg.Name, Opening: cfg.Opening,
		Prompt: cfg.SystemPrompt, ModelID: cfg.ModelID, MaxSteps: cfg.MaxSteps,
	}
	if sess.MaxSteps < 1 {
		sess.MaxSteps = maxToolSteps
	}
	if sess.MaxSteps > agentMaxSteps {
		sess.MaxSteps = agentMaxSteps
	}
	ctx := c.Request().Context()
	sess.KBIDs, sess.Dropped = h.filterAgentKBs(ctx, p, cfg.KBIDs, sess.Dropped)
	sess.ToolIDs, sess.Dropped = h.filterAgentTools(ctx, p, cfg.ToolIDs, sess.Dropped)
	return sess, nil
}

func (h *convHandlers) filterAgentKBs(ctx context.Context, p *auth.Principal, ids []int, dropped []schema.AgentDropped) ([]int, []schema.AgentDropped) {
	out := []int{}
	if len(ids) == 0 {
		return out, dropped
	}
	names := map[int]string{}
	if ks, err := h.db.KnowledgeBase.Query().Where(knowledgebase.IDIn(ids...), knowledgebase.OrgID(p.OrgID)).All(ctx); err == nil {
		for _, k := range ks {
			names[k.ID] = k.Name
		}
	}
	var access *kb.Access
	if h.ret != nil {
		if a, err := kb.LoadAccess(ctx, h.ret.AZ, p); err == nil {
			access = a
		}
	}
	for _, id := range ids {
		if access == nil || !access.Has(id, rbac.BitView) {
			dropped = append(dropped, schema.AgentDropped{Type: "kb", ID: id, Name: names[id], Reason: "no_view"})
			continue
		}
		if _, ok := names[id]; !ok {
			dropped = append(dropped, schema.AgentDropped{Type: "kb", ID: id, Reason: "missing"})
			continue
		}
		out = append(out, id)
	}
	return out, dropped
}

func (h *convHandlers) filterAgentTools(ctx context.Context, p *auth.Principal, ids []int, dropped []schema.AgentDropped) ([]int, []schema.AgentDropped) {
	out := []int{}
	if len(ids) == 0 {
		return out, dropped
	}
	if h.az == nil {
		for _, id := range ids {
			dropped = append(dropped, schema.AgentDropped{Type: "tool", ID: id, Reason: "unavailable"})
		}
		return out, dropped
	}
	ts, err := h.db.Tool.Query().Where(tool.IDIn(ids...), tool.OrgID(p.OrgID)).All(ctx)
	if err != nil {
		for _, id := range ids {
			dropped = append(dropped, schema.AgentDropped{Type: "tool", ID: id, Reason: "error"})
		}
		return out, dropped
	}
	byID := map[int]*ent.Tool{}
	for _, t := range ts {
		byID[t.ID] = t
	}
	for _, id := range ids {
		t := byID[id]
		if t == nil {
			dropped = append(dropped, schema.AgentDropped{Type: "tool", ID: id, Reason: "missing"})
			continue
		}
		d, err := h.az.ToolAccess(ctx, p, t)
		if err != nil || !d.Usable {
			reason := "not_usable"
			if err != nil {
				reason = "error"
			}
			dropped = append(dropped, schema.AgentDropped{Type: "tool", ID: id, Name: t.Name, Reason: reason})
			continue
		}
		out = append(out, id)
	}
	return out, dropped
}

func (h *convHandlers) startAgentRun(ctx context.Context, p *auth.Principal, cv *ent.Conversation, asst *ent.Message, sess *agentSession, tgt *gateway.Target, reqID, traceID string) error {
	if sess == nil {
		return nil
	}
	existing, err := h.db.AgentRun.Query().Where(agentrun.MessageID(asst.ID)).Only(ctx)
	if err == nil {
		sess.RunID = existing.ID
		u := existing.Update().SetStatus(agentrun.StatusRunning).SetModelName(tgt.Model.Name).
			SetMaxSteps(sess.MaxSteps).ClearFinishedAt().SetErrorCode("")
		if len(sess.Dropped) > 0 {
			u.SetDropped(sess.Dropped)
		}
		_, err = u.Save(ctx)
		return err
	}
	if !ent.IsNotFound(err) {
		return err
	}
	q := h.db.AgentRun.Create().SetOrgID(p.OrgID).SetAgentID(sess.AgentID).SetAgentVersion(sess.Version).
		SetDebug(sess.Debug).SetConversationID(cv.ID).SetMessageID(asst.ID).SetUserID(p.ID).
		SetTraceID(traceID).SetRequestID(reqID).SetModelName(tgt.Model.Name).SetStatus(agentrun.StatusRunning).
		SetMaxSteps(sess.MaxSteps)
	if len(sess.Dropped) > 0 {
		q.SetDropped(sess.Dropped)
	}
	run, err := q.Save(ctx)
	if err != nil {
		return err
	}
	sess.RunID = run.ID
	return nil
}

func (h *convHandlers) finishAgentRun(ctx context.Context, sess *agentSession, st *toolState, usage gateway.Usage, outcome, errCode string) {
	if sess == nil || sess.RunID == 0 {
		return
	}
	status := agentrun.StatusDone
	switch outcome {
	case "awaiting_approval":
		status = agentrun.StatusAwaitingApproval
	case "stopped":
		status = agentrun.StatusStopped
	case "error":
		status = agentrun.StatusError
	}
	steps := 0
	calls := 0
	if st != nil {
		steps, calls = st.Steps, len(st.Calls)
	}
	u := h.db.AgentRun.UpdateOneID(sess.RunID).SetStatus(status).SetSteps(steps).SetToolCalls(calls).
		SetPromptTokens(usage.PromptTokens).SetCompletionTokens(usage.CompletionTokens).
		SetStepLimited(sess.MaxSteps > 0 && steps >= sess.MaxSteps && calls > 0)
	if len(sess.Dropped) > 0 {
		u.SetDropped(sess.Dropped)
	}
	if errCode != "" {
		u.SetErrorCode(errCode)
	}
	if status != agentrun.StatusAwaitingApproval && status != agentrun.StatusRunning {
		u.SetFinishedAt(time.Now())
	}
	_ = u.Exec(ctx)
}

func (h *convHandlers) setAgentOnCreate(c echo.Context, p *auth.Principal, q *ent.ConversationCreate, agentID int, debug bool) error {
	a, cfg, _, err := h.bindAgent(c, p, agentID, debug, false)
	if err != nil {
		return err
	}
	q.SetAgentID(agentID).SetAgentDebug(debug).SetKBIds([]int{}).SetToolIds([]int{})
	if cfg.ModelID != nil {
		if m, err := h.db.Model.Get(c.Request().Context(), *cfg.ModelID); err == nil {
			if _, err := h.ch.allowed(c, p, m.Name); err == nil {
				q.SetModelID(m.ID)
			}
		}
	}
	return audit.Record(c, audit.Entry{Action: "chat.conversation.bind_agent", Phase: "attempt", ResourceType: rbac.ResAgent,
		ResourceID: strconv.Itoa(agentID), Detail: map[string]any{"agent_id": agentID, "debug": debug, "published_version": a.PublishedVersion}})
}

func (h *convHandlers) setAgentOnUpdate(c echo.Context, p *auth.Principal, u *ent.ConversationUpdateOne, agentID int, debug bool) error {
	a, cfg, _, err := h.bindAgent(c, p, agentID, debug, false)
	if err != nil {
		return err
	}
	u.SetAgentID(agentID).SetAgentDebug(debug).SetKBIds([]int{}).SetToolIds([]int{})
	if cfg.ModelID != nil {
		if m, err := h.db.Model.Get(c.Request().Context(), *cfg.ModelID); err == nil {
			if _, err := h.ch.allowed(c, p, m.Name); err == nil {
				u.SetModelID(m.ID)
			}
		}
	} else {
		u.ClearModelID()
	}
	return audit.Record(c, audit.Entry{Action: "chat.conversation.bind_agent", Phase: "attempt", ResourceType: rbac.ResAgent,
		ResourceID: strconv.Itoa(agentID), Detail: map[string]any{"agent_id": agentID, "debug": debug, "published_version": a.PublishedVersion}})
}

func agentStepsNotice(n int) string {
	return fmt.Sprintf("工具调用已达 %d 步上限，已停止继续调用工具", n)
}

func mergeAgentAudit(detail map[string]any, sess *agentSession) {
	if sess == nil {
		return
	}
	detail["agent_id"] = sess.AgentID
	detail["agent_version"] = sess.Version
	detail["agent_debug"] = sess.Debug
	detail["agent_name"] = sess.Name
	detail["max_steps"] = sess.MaxSteps
	if sess.RunID > 0 {
		detail["agent_run_id"] = sess.RunID
	}
	if len(sess.Dropped) > 0 {
		detail["agent_dropped"] = sess.Dropped
	}
}

// pickAgentModel 智能体会话优先用配置的模型；不可用则回退默认并提示。
func (h *convHandlers) pickAgentModel(c echo.Context, p *auth.Principal, sess *agentSession, explicit string) (*gateway.Target, string, error) {
	if explicit != "" {
		tgt, err := h.ch.allowed(c, p, explicit)
		return tgt, "", err
	}
	if sess != nil && sess.ModelID != nil {
		if m, err := h.db.Model.Get(c.Request().Context(), *sess.ModelID); err == nil {
			tgt, err := h.ch.allowed(c, p, m.Name)
			if err == nil {
				return tgt, "", nil
			}
			if !errors.Is(err, authz.ErrNotFound) {
				return nil, "", err
			}
		} else if !ent.IsNotFound(err) {
			return nil, "", err
		}
		tgt, err := h.ch.allowed(c, p, "")
		if errors.Is(err, authz.ErrNotFound) {
			return nil, "", errNoModel
		}
		if err != nil {
			return nil, "", err
		}
		return tgt, "智能体配置的模型已不可用，已改用默认模型「" + tgt.Model.Name + "」", nil
	}
	tgt, err := h.ch.allowed(c, p, "")
	if errors.Is(err, authz.ErrNotFound) {
		return nil, "", errNoModel
	}
	return tgt, "", err
}
