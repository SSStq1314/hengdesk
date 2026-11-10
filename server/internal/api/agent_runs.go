package api

// 智能体运行记录查询（M1 第六块 PR3，FR-28 / AC-11）。
// 有 manage 的人可看本智能体的运行列表与详情；详情含 trace_id，审计员可在审计中心按 trace 回放。

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/agentrun"
	"github.com/SSStq1314/hengdesk/server/internal/ent/schema"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

type agentRunView struct {
	ID               int                   `json:"id"`
	AgentID          int                   `json:"agent_id"`
	AgentVersion     int                   `json:"agent_version"`
	Debug            bool                  `json:"debug"`
	ConversationID   int                   `json:"conversation_id"`
	MessageID        int                   `json:"message_id"`
	UserID           int                   `json:"user_id"`
	TraceID          string                `json:"trace_id"`
	RequestID        string                `json:"request_id"`
	ModelName        string                `json:"model_name"`
	Status           string                `json:"status"`
	ErrorCode        string                `json:"error_code,omitempty"`
	Steps            int                   `json:"steps"`
	MaxSteps         int                   `json:"max_steps"`
	ToolCalls        int                   `json:"tool_calls"`
	StepLimited      bool                  `json:"step_limited,omitempty"`
	PromptTokens     int                   `json:"prompt_tokens"`
	CompletionTokens int                   `json:"completion_tokens"`
	Dropped          []schema.AgentDropped `json:"dropped,omitempty"`
	StartedAt        time.Time             `json:"started_at"`
	FinishedAt       *time.Time            `json:"finished_at,omitempty"`
}

func toAgentRun(r *ent.AgentRun) agentRunView {
	return agentRunView{
		ID: r.ID, AgentID: r.AgentID, AgentVersion: r.AgentVersion, Debug: r.Debug,
		ConversationID: r.ConversationID, MessageID: r.MessageID, UserID: r.UserID,
		TraceID: r.TraceID, RequestID: r.RequestID, ModelName: r.ModelName, Status: string(r.Status),
		ErrorCode: r.ErrorCode, Steps: r.Steps, MaxSteps: r.MaxSteps, ToolCalls: r.ToolCalls,
		StepLimited: r.StepLimited, PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens,
		Dropped: r.Dropped, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
}

// listRuns GET /agents/:id/runs — 需要 manage。
func (h *agentHandlers) listRuns(c echo.Context) error {
	_, a, _, err := h.agentOr404(c, rbac.BitManage)
	if err != nil {
		return err
	}
	limit := 50
	if v, err := strconv.Atoi(c.QueryParam("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	rs, err := h.db.AgentRun.Query().Where(agentrun.AgentID(a.ID), agentrun.OrgID(a.OrgID)).
		Order(ent.Desc(agentrun.FieldStartedAt), ent.Desc(agentrun.FieldID)).Limit(limit).All(c.Request().Context())
	if err != nil {
		return err
	}
	out := make([]agentRunView, len(rs))
	for i, r := range rs {
		out[i] = toAgentRun(r)
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

// getRun GET /agents/:id/runs/:rid — 需要 manage；越权 / 不存在 404。
func (h *agentHandlers) getRun(c echo.Context) error {
	_, a, _, err := h.agentOr404(c, rbac.BitManage)
	if err != nil {
		return err
	}
	rid, err := strconv.Atoi(c.Param("rid"))
	if err != nil || rid <= 0 {
		return authz.Deny(c, "agent_run:bad_id")
	}
	r, err := h.db.AgentRun.Query().Where(agentrun.ID(rid), agentrun.AgentID(a.ID), agentrun.OrgID(a.OrgID)).Only(c.Request().Context())
	if ent.IsNotFound(err) {
		return authz.Deny(c, "agent_run:missing")
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toAgentRun(r))
}
