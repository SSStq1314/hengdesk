package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"

	"github.com/SSStq1314/hengdesk/server/internal/aigc"
	"github.com/SSStq1314/hengdesk/server/internal/approval"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/docparse"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/agent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/conversation"
	"github.com/SSStq1314/hengdesk/server/internal/ent/conversationfile"
	"github.com/SSStq1314/hengdesk/server/internal/ent/kbfile"
	"github.com/SSStq1314/hengdesk/server/internal/ent/knowledgebase"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
	"github.com/SSStq1314/hengdesk/server/internal/ent/schema"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/kb"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/usage"
)

// 对话（M1 第三块 PR1，FR-14）。会话只属于创建人：所有接口按 user_id 过滤，非本人 / 不存在 / 已删除一律 404。
// 发消息复用内部 Chat API 的调用链：模型 ACL（无权限 404）→ 预算预检 → 审计 attempt（fail-closed）→
// 网关（重试 / 有权限的备用模型 / 出站白名单）→ 审计 result + usage_records。
const (
	maxMessageChars = 32000 // 单条用户消息上限（字符）
	historyLimit    = 50    // 发给模型的历史消息条数上限（含本条）
	convListLimit   = 200
)

type convHandlers struct {
	db     *ent.Client
	ch     *chatHandlers
	fs     FileStore
	lim    FileLimits
	parser *docparse.Extractor
	r      redis.UniversalClient
	ret    *kb.Retriever
	// az / ap 工具授权判定与审批服务（第五块 PR4）；为空时会话不提供工具。
	az *authz.Service
	ap *approval.Service
}

type convView struct {
	ID            int       `json:"id"`
	Title         string    `json:"title"`
	ModelID       *int      `json:"model_id"`
	KBIDs         []int     `json:"kb_ids"`
	ToolIDs       []int     `json:"tool_ids"`
	AgentID       *int      `json:"agent_id,omitempty"`
	AgentDebug    bool      `json:"agent_debug,omitempty"`
	AgentName     string    `json:"agent_name,omitempty"`
	Opening       string    `json:"opening,omitempty"`
	AgentReadonly bool      `json:"agent_readonly,omitempty"` // D-45：智能体停用/删除后会话只读
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type msgView struct {
	ID               int       `json:"id"`
	Role             string    `json:"role"`
	Content          string    `json:"content"`
	Status           string    `json:"status"`
	ModelName        string    `json:"model_name,omitempty"`
	FallbackFrom     string    `json:"fallback_from,omitempty"`
	ErrorCode        string    `json:"error_code,omitempty"`
	Error            string    `json:"error,omitempty"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	CreatedAt        time.Time `json:"created_at"`
	// ContextTruncated 仅出现在 done 事件：本轮会话文件内容因过长被截断（UI 提示用户）。
	ContextTruncated bool `json:"context_truncated,omitempty"`
	// HistoryTruncated 仅出现在 done 事件：较早的历史消息因超出模型上下文窗口未发送（D-40 第 4 条）。
	HistoryTruncated bool `json:"history_truncated,omitempty"`
	// Notice 仅出现在 done 事件：记住的模型已不可用、已回退默认模型等需要告知用户的提示（D-40 第 8 条）。
	Notice string `json:"notice,omitempty"`
	// Citations 本条回复引用的知识库片段（读取时按当前权限过滤）；ToolCalls 为第五块预留，当前始终为空。
	Citations []Citation `json:"citations,omitempty"`
	// KBUngrounded 绑定了知识库但本条回答未引用知识库资料（D-42）。
	KBUngrounded bool       `json:"kb_ungrounded,omitempty"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	// UsageEstimated 上游未返回 usage，token 为估算值（D-41 第 4 条，界面标「估算」）。
	UsageEstimated bool `json:"usage_estimated,omitempty"`
	// AgentDropped 调试会话中因调用者无权而被静默剔除的绑定资源（正式会话不返回）。
	AgentDropped []schema.AgentDropped `json:"agent_dropped,omitempty"`
	// AIGC 本条为 AI 生成内容（FR-30）；AIGCBadge 为界面显式角标文案。
	AIGC      bool   `json:"aigc,omitempty"`
	AIGCBadge string `json:"aigc_badge,omitempty"`
}

// enrichConv 补齐智能体名称与开场白（正式取发布版，调试取草稿）；停用/删除时标记只读。
func (h *convHandlers) enrichConv(c echo.Context, p *auth.Principal, cv *ent.Conversation) (convView, error) {
	v := toConv(cv)
	if cv.AgentID == nil {
		return v, nil
	}
	_, cfg, _, err := h.bindAgent(c, p, *cv.AgentID, cv.AgentDebug, true)
	if err != nil {
		if errors.Is(err, errAgentReadonly) {
			v.AgentReadonly = true
			// 尽量带上名称，便于前端提示。
			if a, e := h.db.Agent.Query().Where(agent.ID(*cv.AgentID), agent.OrgID(p.OrgID)).Only(c.Request().Context()); e == nil {
				v.AgentName = a.Name
				if !cv.AgentDebug {
					if pub, e2 := (&agentHandlers{db: h.db, az: h.az}).published(c.Request().Context(), a); e2 == nil && pub != nil {
						v.AgentName, v.Opening = pub.Name, pub.Opening
					}
				}
			}
			return v, nil
		}
		// 绑定后失去权限：仍返回基本字段，名称留空（发消息时会 404）。
		return v, nil
	}
	v.AgentName, v.Opening = cfg.Name, cfg.Opening
	return v, nil
}

func toConv(c *ent.Conversation) convView {
	ids := c.KBIds
	if ids == nil {
		ids = []int{}
	}
	tids := c.ToolIds
	if tids == nil {
		tids = []int{}
	}
	return convView{ID: c.ID, Title: c.Title, ModelID: c.ModelID, KBIDs: ids, ToolIDs: tids,
		AgentID: c.AgentID, AgentDebug: c.AgentDebug, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

func toMsgAIGC(m *ent.Message, st aigc.Settings) msgView {
	v := msgView{ID: m.ID, Role: string(m.Role), Content: m.Content, Status: string(m.Status), ModelName: m.ModelName,
		FallbackFrom: m.FallbackFrom, ErrorCode: m.ErrorCode, Error: m.Error, PromptTokens: m.PromptTokens, UsageEstimated: m.UsageEstimated,
		CompletionTokens: m.CompletionTokens, CreatedAt: m.CreatedAt, Citations: m.Citations, KBUngrounded: m.KBUngrounded, ToolCalls: m.ToolCalls}
	if m.Role == message.RoleAssistant {
		st = st.Normalize()
		v.AIGC, v.AIGCBadge = true, st.Badge
	}
	return v
}

// own 取当前用户自己的、未删除的会话；其它情况记越权审计并返回 404（不区分不存在与非本人）。
func (h *convHandlers) own(c echo.Context, p *auth.Principal) (*ent.Conversation, error) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return nil, authz.Deny(c, "conversation:bad_id")
	}
	cv, err := h.db.Conversation.Query().Where(conversation.ID(id), conversation.UserID(p.ID),
		conversation.OrgID(p.OrgID), conversation.DeletedAtIsNil()).Only(c.Request().Context())
	if ent.IsNotFound(err) {
		return nil, authz.Deny(c, "conversation:owner")
	}
	return cv, err
}

func principal(c echo.Context) (*auth.Principal, error) {
	p := auth.Current(c)
	if p == nil {
		return nil, authz.ErrUnauthenticated
	}
	return p, nil
}

func (h *convHandlers) list(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	cs, err := h.db.Conversation.Query().Where(conversation.UserID(p.ID), conversation.OrgID(p.OrgID), conversation.DeletedAtIsNil()).
		Order(ent.Desc(conversation.FieldUpdatedAt), ent.Desc(conversation.FieldID)).Limit(convListLimit).All(c.Request().Context())
	if err != nil {
		return err
	}
	out := make([]convView, len(cs))
	for i, cv := range cs {
		out[i] = toConv(cv)
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

type convInput struct {
	Title *string `json:"title"`
	Model string  `json:"model"`
	// KBIDs 选用的知识库（最多 maxConvKBs 个）；传 [] 清空，不传不变。绑定智能体时忽略。
	KBIDs *[]int `json:"kb_ids"`
	// ToolIDs 选用的工具（第五块 PR4，最多 maxConvTools 个，必须当前可用）；传 [] 清空，不传不变。绑定智能体时忽略。
	ToolIDs *[]int `json:"tool_ids"`
	// AgentID 绑定的智能体；传 0 或配合 clear_agent 解除。正式会话需 use+已发布，调试需 edit。
	AgentID    *int  `json:"agent_id"`
	AgentDebug *bool `json:"agent_debug"`
	ClearAgent bool  `json:"clear_agent"`
}

const maxConvKBs = 5

// checkKBs 校验会话要选用的知识库：去重、最多 5 个、每个都必须本人有 view 且未删除（否则 404，不区分不存在与无权）。
func (h *convHandlers) checkKBs(c echo.Context, p *auth.Principal, ids []int) ([]int, error) {
	seen, out := map[int]bool{}, []int{}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) > maxConvKBs {
		return nil, bad("最多选择 " + strconv.Itoa(maxConvKBs) + " 个知识库")
	}
	if len(out) == 0 {
		return out, nil
	}
	if h.ret == nil {
		return nil, authz.Deny(c, "kb:unavailable")
	}
	ctx := c.Request().Context()
	a, err := kb.LoadAccess(ctx, h.ret.AZ, p)
	if err != nil {
		return nil, err
	}
	for _, id := range out {
		if !a.Has(id, rbac.BitView) {
			return nil, authz.Deny(c, "kb:view")
		}
	}
	n, err := h.db.KnowledgeBase.Query().Where(knowledgebase.IDIn(out...), knowledgebase.OrgID(p.OrgID), knowledgebase.DeletedAtIsNil()).Count(ctx)
	if err != nil {
		return nil, err
	}
	if n != len(out) {
		return nil, authz.Deny(c, "kb:not_found")
	}
	return out, nil
}

func cleanTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > 60 {
		s = string([]rune(s)[:60])
	}
	return s
}

func (h *convHandlers) create(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in convInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	q := h.db.Conversation.Create().SetOrgID(p.OrgID).SetUserID(p.ID)
	if in.Title != nil {
		q.SetTitle(cleanTitle(*in.Title))
	}
	boundAgent := in.AgentID != nil && *in.AgentID > 0
	if boundAgent {
		debug := in.AgentDebug != nil && *in.AgentDebug
		if err := h.setAgentOnCreate(c, p, q, *in.AgentID, debug); err != nil {
			return err
		}
	} else {
		if in.Model != "" {
			tgt, err := h.ch.allowed(c, p, in.Model)
			if err != nil {
				return err
			}
			q.SetModelID(tgt.Model.ID)
		}
		if in.KBIDs != nil {
			ids, err := h.checkKBs(c, p, *in.KBIDs)
			if err != nil {
				return err
			}
			q.SetKBIds(ids)
		}
		if in.ToolIDs != nil {
			ids, err := h.checkTools(c, p, *in.ToolIDs)
			if err != nil {
				return err
			}
			q.SetToolIds(ids)
		}
	}
	cv, err := q.Save(c.Request().Context())
	if err != nil {
		return err
	}
	v, err := h.enrichConv(c, p, cv)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, v)
}

func (h *convHandlers) get(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	cv, err := h.own(c, p)
	if err != nil {
		return err
	}
	ms, err := h.db.Message.Query().Where(message.ConversationID(cv.ID), message.SupersededAtIsNil()).Order(ent.Asc(message.FieldID)).All(c.Request().Context())
	if err != nil {
		return err
	}
	st, err := aigc.Load(c.Request().Context(), h.db)
	if err != nil {
		return err
	}
	out := make([]msgView, len(ms))
	var lastMeta aigc.Meta
	for i, m := range ms {
		out[i] = toMsgAIGC(m, st)
		if m.Role == message.RoleAssistant {
			lastMeta = aigc.BuildMeta(st, m.RequestID, fmt.Sprintf("msg-%d", m.ID))
		}
	}
	if err := h.filterCitations(c.Request().Context(), p, out); err != nil {
		return err
	}
	fs, err := h.files(c.Request().Context(), cv.ID)
	if err != nil {
		return err
	}
	fv := make([]fileView, len(fs))
	for i, f := range fs {
		fv[i] = toFile(f)
	}
	v, err := h.enrichConv(c, p, cv)
	if err != nil {
		return err
	}
	if lastMeta.ProduceID != "" {
		c.Response().Header().Set(aigc.HeaderName, aigc.HeaderValue(lastMeta))
	}
	return c.JSON(http.StatusOK, map[string]any{"conversation": v, "messages": out, "files": fv, "aigc": map[string]string{"badge": st.Badge, "suffix": st.Suffix}})
}

func (h *convHandlers) update(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	cv, err := h.own(c, p)
	if err != nil {
		return err
	}
	var in convInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	u := cv.Update()
	if in.Title != nil {
		u.SetTitle(cleanTitle(*in.Title))
	}
	if in.ClearAgent {
		u.ClearAgentID().SetAgentDebug(false)
	} else if in.AgentID != nil && *in.AgentID > 0 {
		debug := cv.AgentDebug
		if in.AgentDebug != nil {
			debug = *in.AgentDebug
		}
		if err := h.setAgentOnUpdate(c, p, u, *in.AgentID, debug); err != nil {
			return err
		}
	} else if in.AgentDebug != nil && cv.AgentID != nil {
		if err := h.setAgentOnUpdate(c, p, u, *cv.AgentID, *in.AgentDebug); err != nil {
			return err
		}
	} else if cv.AgentID == nil {
		if in.Model != "" {
			tgt, err := h.ch.allowed(c, p, in.Model)
			if err != nil {
				return err
			}
			u.SetModelID(tgt.Model.ID)
		}
		if in.KBIDs != nil {
			ids, err := h.checkKBs(c, p, *in.KBIDs)
			if err != nil {
				return err
			}
			u.SetKBIds(ids)
		}
		if in.ToolIDs != nil {
			ids, err := h.checkTools(c, p, *in.ToolIDs)
			if err != nil {
				return err
			}
			u.SetToolIds(ids)
		}
	}
	cv, err = u.Save(c.Request().Context())
	if err != nil {
		return err
	}
	v, err := h.enrichConv(c, p, cv)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, v)
}

// remove 软删除：用户侧不可见（之后访问 404），消息保留供审计链路还原；会话文件一并软删除。物理清理随保留期策略（待产品确认）。
func (h *convHandlers) remove(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	cv, err := h.own(c, p)
	if err != nil {
		return err
	}
	ctx, now := c.Request().Context(), time.Now()
	if err := withTx(ctx, h.db, func(tx *ent.Tx) error {
		if err := tx.Conversation.UpdateOneID(cv.ID).SetDeletedAt(now).Exec(ctx); err != nil {
			return err
		}
		return tx.ConversationFile.Update().Where(conversationfile.ConversationID(cv.ID), conversationfile.DeletedAtIsNil()).
			SetDeletedAt(now).Exec(ctx)
	}); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

type sendInput struct {
	Content string `json:"content"`
	Model   string `json:"model"`
}

// send POST /conversations/:id/messages：SSE 流式返回。事件：token（增量）、done（最终消息）、error（流开始后出错）。
// 流开始前失败返回 JSON 错误（与内部 Chat API 一致）。停止生成 = 客户端断开连接：已生成部分以 status=stopped 落库并照常计量。
// 同一会话同时只允许一条在生成（D-40 第 7 条）：锁在 Valkey，其它请求 409。
func (h *convHandlers) send(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	cv, err := h.own(c, p)
	if err != nil {
		return err
	}
	r := c.Request()
	r.Body = http.MaxBytesReader(c.Response(), r.Body, 1<<20)
	var in sendInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return bad("请求格式错误或超过 1 MiB")
	}
	in.Content = strings.TrimSpace(in.Content)
	if in.Content == "" {
		return bad("消息不能为空")
	}
	if utf8.RuneCountInString(in.Content) > maxMessageChars {
		return bad("消息过长（上限 " + strconv.Itoa(maxMessageChars) + " 字）")
	}
	ctx := r.Context()
	unlock, err := h.lock(ctx, cv.ID)
	if err != nil {
		return err
	}
	defer unlock()
	if aw, err := h.awaitingLast(ctx, cv.ID); err != nil || aw {
		if err != nil {
			return err
		}
		return errAwaiting
	}
	sess, err := h.resolveAgent(c, p, cv)
	if err != nil {
		return err
	}
	var tgt *gateway.Target
	var notice string
	if sess != nil {
		tgt, notice, err = h.pickAgentModel(c, p, sess, in.Model)
	} else {
		tgt, notice, err = h.pickModel(c, p, cv, in.Model)
	}
	if err != nil {
		return err
	}
	if err := budgetGate(c, h.ch.bs, h.db, tgt.Model.ID, tgt.Model.Name); err != nil || c.Response().Committed {
		return err
	}
	detail := map[string]any{"model": tgt.Model.Name, "chars": utf8.RuneCountInString(in.Content), "model_reset": notice != ""}
	mergeAgentAudit(detail, sess)
	if err := audit.Record(c, audit.Entry{Action: "chat.message.send", Phase: "attempt", ResourceType: "conversation",
		ResourceID: strconv.Itoa(cv.ID), Detail: detail}); err != nil {
		return err // fail-closed
	}
	reqID := c.Response().Header().Get(echo.HeaderXRequestID)
	var asst *ent.Message
	if err := withTx(ctx, h.db, func(tx *ent.Tx) error {
		if _, err := tx.Message.Create().SetConversationID(cv.ID).SetRole(message.RoleUser).SetContent(in.Content).
			SetRequestID(reqID).Save(ctx); err != nil {
			return err
		}
		var err error
		asst, err = tx.Message.Create().SetConversationID(cv.ID).SetRole(message.RoleAssistant).SetStatus(message.StatusStreaming).
			SetModelName(tgt.Model.Name).SetRequestID(reqID).Save(ctx)
		if err != nil {
			return err
		}
		u := tx.Conversation.UpdateOne(cv).SetModelID(tgt.Model.ID)
		if cv.Title == "" {
			u.SetTitle(cleanTitle(in.Content))
		}
		return u.Exec(ctx)
	}); err != nil {
		return err
	}
	return h.generate(c, p, cv, tgt, asst, "chat.message.send", notice, false, nil)
}

type regenInput struct {
	MessageID int `json:"message_id"`
	// SkipKB 本次不使用知识库继续（D-42）：检索失败后用户确认跳过，本轮不检索、回答标注「未引用知识库资料」。
	SkipKB bool `json:"skip_kb"`
}

// regenerate POST /conversations/:id/regenerate：重新生成 / 重试最后一条回复（D-40 第 6 条）。
// 只允许针对会话最后一条（未被替换的）assistant 消息；旧回复软保留（superseded_at），UI 与后续上下文只用最新一条。
// 与发送相同：会话锁 → 模型权限（含记忆模型失效回退）→ 预算 → 审计 attempt（fail-closed）。失败消息的「重试」走同一接口。
func (h *convHandlers) regenerate(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	cv, err := h.own(c, p)
	if err != nil {
		return err
	}
	var in regenInput
	if err := c.Bind(&in); err != nil || in.MessageID <= 0 {
		return bad("请求格式错误")
	}
	ctx := c.Request().Context()
	unlock, err := h.lock(ctx, cv.ID)
	if err != nil {
		return err
	}
	defer unlock()
	last, err := h.db.Message.Query().Where(message.ConversationID(cv.ID), message.SupersededAtIsNil()).
		Order(ent.Desc(message.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return authz.Deny(c, "message:not_found")
	}
	if err != nil {
		return err
	}
	if last.ID != in.MessageID {
		old, err := h.db.Message.Query().Where(message.ID(in.MessageID), message.ConversationID(cv.ID)).Exist(ctx)
		if err != nil {
			return err
		}
		if !old {
			return authz.Deny(c, "message:not_found")
		}
		return echo.NewHTTPError(http.StatusConflict, "只能重新生成最后一条回复")
	}
	if last.Status == message.StatusAwaitingApproval {
		return errAwaiting
	}
	if last.Role != message.RoleAssistant || last.Status == message.StatusStreaming {
		return echo.NewHTTPError(http.StatusConflict, "只能重新生成最后一条已结束的回复")
	}
	sess, err := h.resolveAgent(c, p, cv)
	if err != nil {
		return err
	}
	var tgt *gateway.Target
	var notice string
	if sess != nil {
		tgt, notice, err = h.pickAgentModel(c, p, sess, "")
	} else {
		tgt, notice, err = h.pickModel(c, p, cv, "")
	}
	if err != nil {
		return err
	}
	if err := budgetGate(c, h.ch.bs, h.db, tgt.Model.ID, tgt.Model.Name); err != nil || c.Response().Committed {
		return err
	}
	kind := "regenerate"
	if last.Status == message.StatusError {
		kind = "retry"
	}
	if err := audit.Record(c, audit.Entry{Action: "chat.message.regenerate", Phase: "attempt", ResourceType: "conversation",
		ResourceID: strconv.Itoa(cv.ID), Detail: map[string]any{"model": tgt.Model.Name, "kind": kind,
			"replaces_message_id": last.ID, "model_reset": notice != "", "skip_kb": in.SkipKB}}); err != nil {
		return err // fail-closed
	}
	reqID := c.Response().Header().Get(echo.HeaderXRequestID)
	var asst *ent.Message
	if err := withTx(ctx, h.db, func(tx *ent.Tx) error {
		n, err := tx.Message.Update().Where(message.ID(last.ID), message.SupersededAtIsNil()).SetSupersededAt(time.Now()).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return echo.NewHTTPError(http.StatusConflict, "该回复已被替换")
		}
		asst, err = tx.Message.Create().SetConversationID(cv.ID).SetRole(message.RoleAssistant).SetStatus(message.StatusStreaming).
			SetModelName(tgt.Model.Name).SetRequestID(reqID).Save(ctx)
		if err != nil {
			return err
		}
		return tx.Conversation.UpdateOne(cv).SetModelID(tgt.Model.ID).Exec(ctx)
	}); err != nil {
		return err
	}
	return h.generate(c, p, cv, tgt, asst, "chat.message.regenerate", notice, in.SkipKB, map[string]any{"kind": kind, "replaces_message_id": last.ID, "skip_kb": in.SkipKB})
}

// filterCitations 读取历史消息时按当前权限过滤引用：文件已删除、知识库已删除或已失去 view 的引用整条去掉（不暴露文件名）。
func (h *convHandlers) filterCitations(ctx context.Context, p *auth.Principal, ms []msgView) error {
	need := false
	for _, m := range ms {
		need = need || len(m.Citations) > 0
	}
	if !need {
		return nil
	}
	ok := map[int]bool{}
	if h.ret != nil {
		a, err := kb.LoadAccess(ctx, h.ret.AZ, p)
		if err != nil {
			return err
		}
		var fids []int
		for _, m := range ms {
			for _, ci := range m.Citations {
				fids = append(fids, ci.FileID)
			}
		}
		fs, err := h.db.KBFile.Query().Where(kbfile.IDIn(fids...), kbfile.OrgID(p.OrgID)).All(ctx)
		if err != nil {
			return err
		}
		live, err := h.db.KnowledgeBase.Query().Where(knowledgebase.OrgID(p.OrgID), knowledgebase.DeletedAtIsNil(),
			knowledgebase.IDIn(kbOfFiles(fs)...)).IDs(ctx)
		if err != nil {
			return err
		}
		alive := map[int]bool{}
		for _, id := range live {
			alive[id] = true
		}
		for _, f := range fs {
			ok[f.ID] = alive[f.KBID] && a.CanSeeFile(f)
		}
	}
	for i := range ms {
		var keep []Citation
		for _, ci := range ms[i].Citations {
			if ok[ci.FileID] {
				keep = append(keep, ci)
			}
		}
		ms[i].Citations = keep
	}
	return nil
}

func kbOfFiles(fs []*ent.KBFile) []int {
	out := make([]int, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.KBID)
	}
	return out
}

// pickModel 选本轮模型。显式指定的模型按权限校验（无权限 404）。未指定时用会话记住的模型；
// 若它已不可用（被删 / 停用 / 收回权限），回退到默认模型并返回提示（D-40 第 8 条）；默认模型也不可用时返回明确错误。
func (h *convHandlers) pickModel(c echo.Context, p *auth.Principal, cv *ent.Conversation, explicit string) (*gateway.Target, string, error) {
	if explicit != "" {
		tgt, err := h.ch.allowed(c, p, explicit)
		return tgt, "", err
	}
	if cv.ModelID == nil {
		tgt, err := h.ch.allowed(c, p, "")
		if errors.Is(err, authz.ErrNotFound) {
			return nil, "", errNoModel
		}
		return tgt, "", err
	}
	if m, err := h.db.Model.Get(c.Request().Context(), *cv.ModelID); err == nil {
		tgt, err := h.ch.allowed(c, p, m.Name)
		if err == nil || !errors.Is(err, authz.ErrNotFound) {
			return tgt, "", err
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
	return tgt, "此前使用的模型已不可用，已改用默认模型「" + tgt.Model.Name + "」", nil
}

var errNoModel = echo.NewHTTPError(http.StatusUnprocessableEntity, "此前使用的模型已不可用，且没有可用的默认模型，请联系管理员开通模型权限")

const genLockTTL = 60 * time.Second

var unlockScript = redis.NewScript(`if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) end return 0`)

// lock 会话生成锁（D-40 第 7 条）：Valkey SET NX + 过期时间，生成期间定时续期；进程崩溃时锁自动过期。
// Valkey 不可用时 fail-closed（503），不在无锁状态下生成。
func (h *convHandlers) lock(ctx context.Context, convID int) (func(), error) {
	if h.r == nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "会话锁不可用")
	}
	key := "hengdesk:chat:gen:" + strconv.Itoa(convID)
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	tok := hex.EncodeToString(b)
	ok, err := h.r.SetNX(ctx, key, tok, genLockTTL).Result()
	if err != nil {
		h.ch.log.Error("conversation lock failed", "conversation", convID, "err", err)
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "会话锁不可用，请稍后重试")
	}
	if !ok {
		return nil, echo.NewHTTPError(http.StatusConflict, "该会话正在生成回复，请等待完成或先停止")
	}
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(genLockTTL / 3)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_ = h.r.Expire(context.Background(), key, genLockTTL).Err()
			}
		}
	}()
	return func() {
		close(stop)
		_ = unlockScript.Run(context.Background(), h.r, []string{key}, tok).Err()
	}, nil
}

// generate 组上下文、流式调用模型并落库最终状态；写 result 审计。上下文只用未被替换的消息。
func (h *convHandlers) generate(c echo.Context, p *auth.Principal, cv *ent.Conversation, tgt *gateway.Target, asst *ent.Message,
	action, notice string, skipKB bool, extra map[string]any) error {
	return h.generateWith(c, p, cv, tgt, asst, action, notice, skipKB, extra, nil)
}

// generateWith 同 generate；rs 非空时从挂起的工具循环状态继续（审批结束后，见 convtools.go）。
func (h *convHandlers) generateWith(c echo.Context, p *auth.Principal, cv *ent.Conversation, tgt *gateway.Target, asst *ent.Message,
	action, notice string, skipKB bool, extra map[string]any, rs *toolState) error {
	ctx := c.Request().Context()
	sess, err := h.resolveAgent(c, p, cv)
	if err != nil {
		return err
	}
	work := *cv
	stepLimit := maxToolSteps
	if sess != nil {
		work.KBIds, work.ToolIds = sess.KBIDs, sess.ToolIDs
		stepLimit = sess.MaxSteps
		// 让本轮模型 / embedding 用量带上 agent_id（FR-13）。
		r := c.Request()
		c.SetRequest(r.WithContext(usage.WithAgentID(r.Context(), sess.AgentID)))
	}
	files, err := h.files(ctx, cv.ID)
	if err != nil {
		return err
	}
	hist, err := h.db.Message.Query().Where(message.ConversationID(cv.ID), message.SupersededAtIsNil(), message.ContentNEQ(""),
		message.IDLT(asst.ID)).Order(ent.Desc(message.FieldID)).Limit(historyLimit).All(ctx)
	if err != nil {
		return err
	}
	cms := make([]ctxMsg, 0, len(hist)+3)
	if sess != nil && strings.TrimSpace(sess.Prompt) != "" {
		cms = append(cms, ctxMsg{Role: "system", Content: sess.Prompt})
	}
	fctx, ctxCut := fileContext(files, h.lim.TotalChars)
	if fctx != "" {
		cms = append(cms, ctxMsg{Role: "system", Content: fctx})
	}
	reqID := c.Response().Header().Get(echo.HeaderXRequestID)
	traceID := c.Response().Header().Get(audit.HeaderTraceID)
	if traceID == "" {
		traceID = audit.TraceFrom(ctx)
	}
	if err := h.startAgentRun(ctx, p, cv, asst, sess, tgt, reqID, traceID); err != nil {
		return err
	}
	// 知识库检索（FR-17/18）：问题取本轮最新一条用户消息；ACL 每次现算。审计写失败则拒绝（不调用模型）。
	var cites []Citation
	var prelude []sseEvent
	kbInfo := map[string]any{}
	ungrounded := false
	if len(work.KBIds) > 0 && h.ret != nil {
		strict, err := h.strictKBs(ctx, p, work.KBIds)
		if err != nil {
			return err
		}
		if skipKB {
			// D-42：本次不使用知识库继续。开启了「仅依据资料回答」的库不允许跳过。
			if len(strict) > 0 {
				_ = h.db.Message.UpdateOneID(asst.ID).SetStatus(message.StatusError).SetErrorCode("kb_strict").
					SetError(kbStrictSkipMsg).Exec(context.WithoutCancel(ctx))
				h.finishAgentRun(context.WithoutCancel(ctx), sess, &toolState{}, gateway.Usage{}, "error", "kb_strict")
				return echo.NewHTTPError(http.StatusConflict, kbStrictSkipMsg)
			}
			ungrounded = true
			kbInfo = map[string]any{"kb_ids": work.KBIds, "kb_skipped": true}
			cms = append(cms, ctxMsg{Role: "system", Content: kbSkippedPrompt})
		} else {
			q := ""
			for _, m := range hist {
				if m.Role == message.RoleUser {
					q = m.Content
					break
				}
			}
			kr, kerr := h.ret.Retrieve(ctx, p, work.KBIds, q, reqID, 0)
			if kerr != nil {
				// D-42：检索失败仍返回 503（不自动降级）；前端提示「知识库暂时不可用」，由用户选择重试或本次不使用知识库继续。
				code, msg := "kb_failed", "知识库暂时不可用"
				if errors.Is(kerr, kb.ErrRetrieveAudit) {
					code, msg = "audit_failed", "审计写入失败，未检索知识库，也未调用模型"
				}
				_ = h.db.Message.UpdateOneID(asst.ID).SetStatus(message.StatusError).SetErrorCode(code).SetError(msg).Exec(context.WithoutCancel(ctx))
				h.ch.log.Error("kb retrieve failed", "conversation", cv.ID, "err", kerr)
				h.finishAgentRun(context.WithoutCancel(ctx), sess, &toolState{}, gateway.Usage{}, "error", code)
				return c.JSON(http.StatusServiceUnavailable, map[string]any{"message": msg, "error": map[string]string{"code": code, "message": msg}})
			}
			kbInfo = map[string]any{"kb_ids": work.KBIds, "kb_allowed_files": kr.AllowedFiles, "kb_hits": len(kr.Citations), "kb_strategy": kr.Strategy}
			if len(kr.Citations) == 0 {
				if len(strict) > 0 {
					mergeAgentAudit(kbInfo, sess)
					h.finishAgentRun(context.WithoutCancel(ctx), sess, &toolState{}, gateway.Usage{}, "done", "")
					return h.refuseStrict(c, cv, asst, action, notice, strict, kbInfo, extra)
				}
				ungrounded = true
				cms = append(cms, ctxMsg{Role: "system", Content: kbEmptyPrompt})
				if notice != "" {
					notice += "；"
				}
				notice += "所选知识库中没有可用资料"
			} else {
				cms = append(cms, ctxMsg{Role: "system", Content: kbContext(kr.Citations)})
				for _, ci := range kr.Citations {
					mc := Citation{Index: ci.Index, KBID: ci.KBID, KBName: ci.KBName, FileID: ci.FileID, FileName: ci.FileName,
						Seq: ci.Seq, Start: ci.Start, End: ci.End, Heading: ci.Heading, Snippet: ci.Snippet, Score: ci.Score}
					cites = append(cites, mc)
					prelude = append(prelude, sseEvent{event: EventCitation, data: mc})
				}
			}
		}
		kbInfo["kb_ungrounded"] = ungrounded
	}
	for i := len(hist) - 1; i >= 0; i-- {
		cms = append(cms, ctxMsg{Role: string(hist[i].Role), Content: hist[i].Content})
	}
	// D-40 第 4 条：按所选模型的上下文窗口截断，始终保留系统提示词与最新一条用户消息。
	cms, histDropped, estTokens := truncateByTokens(cms, tokenBudget(tgt.Model.ContextWindow, tgt.Model.MaxOutputTokens))
	histCut := histDropped > 0
	msgs := make([]gateway.Message, 0, len(cms))
	for _, m := range cms {
		msgs = append(msgs, textMessage(m.Role, m.Content))
	}
	req := gateway.ChatRequest{Model: tgt.Model.Name, Messages: msgs, Stream: true}

	// 工具循环（第五块 PR4）：每轮模型调用后处理工具调用，直到模型直接作答、需要审批挂起或达到步数上限。
	tools, tmap, err := h.convTools(ctx, p, &work)
	if err != nil {
		return err
	}
	st := rs
	if st == nil {
		st = &toolState{}
	}
	if len(tools) > 0 {
		req.Tools = tools
	}
	summary := ""
	for _, m := range hist {
		if m.Role == message.RoleUser {
			summary = truncRunes(m.Content, 500)
			break
		}
	}
	var buf strings.Builder
	buf.WriteString(st.Content)
	var (
		res       *gateway.ChatResponse
		ge        *gateway.Error
		started   = c.Response().Committed
		used      = tgt
		rerr, ter error
		awaiting  bool
		usage     gateway.Usage
	)
	if len(st.Queue) > 0 {
		awaiting, ter = h.runTools(c, p, cv, asst, tmap, st, summary)
		started = started || c.Response().Committed
	}
	aigcSt, _ := aigc.Load(ctx, h.db)
	aigcSt = aigcSt.Normalize()
	view := func(status string, used, primary *gateway.Target) msgView {
		mv := msgView{ID: asst.ID, Role: "assistant", Content: buf.String(), Status: status, ModelName: used.Model.Name,
			FallbackFrom: fallbackFrom(used, primary), PromptTokens: usage.PromptTokens, UsageEstimated: usage.Estimated,
			CompletionTokens: usage.CompletionTokens, CreatedAt: asst.CreatedAt, ContextTruncated: ctxCut, HistoryTruncated: histCut,
			Notice: notice, Citations: cites, KBUngrounded: ungrounded, ToolCalls: st.Calls,
			AIGC: true, AIGCBadge: aigcSt.Badge}
		if sess != nil && sess.Debug && len(sess.Dropped) > 0 {
			mv.AgentDropped = sess.Dropped
		}
		return mv
	}
	for !awaiting && ter == nil {
		req.Messages = append(msgs[:len(msgs):len(msgs)], st.Trace...)
		last := len(tools) > 0 && st.Steps >= stepLimit
		if last {
			req.ToolChoice = json.RawMessage(`"none"`)
			stepNotice := agentStepsNotice(stepLimit)
			if !strings.Contains(notice, stepNotice) {
				if notice != "" {
					notice += "；"
				}
				notice += stepNotice
			}
		}
		hk := streamHooks{delta: EventToken, prelude: prelude, tap: func(d gateway.Delta) { buf.WriteString(d.Content) },
			done: func(r *gateway.ChatResponse, used, primary *gateway.Target) any {
				if len(tools) > 0 && len(r.Message.ToolCalls) > 0 && !last {
					return nil // 还要执行工具，本轮不发 done
				}
				usage.PromptTokens += r.Usage.PromptTokens
				usage.CompletionTokens += r.Usage.CompletionTokens
				usage.Estimated = usage.Estimated || r.Usage.Estimated
				return view("done", used, primary)
			}}
		prelude = nil
		var s bool
		res, ge, s, used, rerr = h.ch.run(c, p, &req, tgt, hk)
		if ge != nil && !s && c.Response().Committed {
			sse(c, EventError, map[string]any{"error": ge}) // 工具轮次之后的调用失败：流已开始，只能发 error 事件
			s = true
		}
		started = started || s
		if rerr != nil || ge != nil || res == nil {
			break
		}
		if len(tools) == 0 || len(res.Message.ToolCalls) == 0 || last {
			break
		}
		usage.PromptTokens += res.Usage.PromptTokens
		usage.CompletionTokens += res.Usage.CompletionTokens
		usage.Estimated = usage.Estimated || res.Usage.Estimated
		st.Steps++
		am := res.Message
		am.Role = "assistant"
		for i := range am.ToolCalls {
			am.ToolCalls[i].Index = nil
			// 调用 ID 在一条回复内必须唯一（部分上游每轮复用同一个 ID）：重复时加上步数后缀，trace 与结果一致使用新 ID。
			for _, v := range st.Calls {
				if v.ID == am.ToolCalls[i].ID {
					am.ToolCalls[i].ID = fmt.Sprintf("%s_s%d", am.ToolCalls[i].ID, st.Steps)
					break
				}
			}
			if am.ToolCalls[i].Type == "" {
				am.ToolCalls[i].Type = "function"
			}
		}
		st.Trace = append(st.Trace, am)
		st.Queue = am.ToolCalls
		awaiting, ter = h.runTools(c, p, cv, asst, tmap, st, summary)
		started = started || c.Response().Committed
	}
	if res != nil && ge == nil && rerr == nil && !awaiting && ter == nil {
		res.Usage = usage
	}

	// 落库用 WithoutCancel：客户端停止 / 断开后仍要保存已生成部分。
	bg := context.WithoutCancel(ctx)
	u := h.db.Message.UpdateOneID(asst.ID).SetModelName(used.Model.Name).SetFallbackFrom(fallbackFrom(used, tgt)).SetContent(buf.String())
	if len(cites) > 0 {
		u.SetCitations(cites)
	}
	u.SetKBUngrounded(ungrounded)
	stopped := errors.Is(ctx.Err(), context.Canceled)
	if len(st.Calls) > 0 {
		u.SetToolCalls(st.Calls)
	}
	switch {
	case ter != nil && toolAuditErr(ter):
		u.SetStatus(message.StatusError).SetErrorCode("audit_failed").SetError(toolAuditMsg).ClearToolState()
	case ter != nil:
		u.SetStatus(message.StatusError).SetErrorCode("tool_failed").SetError("工具调用处理失败").ClearToolState()
	case awaiting:
		st.Content = buf.String()
		b, _ := json.Marshal(st)
		u.SetStatus(message.StatusAwaitingApproval).SetToolState(b).SetPromptTokens(usage.PromptTokens).
			SetCompletionTokens(usage.CompletionTokens).SetUsageEstimated(usage.Estimated)
	case rerr != nil:
		u.SetStatus(message.StatusError).SetErrorCode("audit_failed").SetError("审计写入失败，未调用模型")
	case ge == nil && res != nil:
		u.ClearToolState()
		u.SetStatus(message.StatusDone).SetPromptTokens(res.Usage.PromptTokens).SetCompletionTokens(res.Usage.CompletionTokens).SetUsageEstimated(res.Usage.Estimated)
		if buf.Len() == 0 {
			u.SetContent(contentText(res.Message.Content))
		}
	case stopped:
		u.SetStatus(message.StatusStopped)
	default:
		u.SetStatus(message.StatusError).SetErrorCode(ge.Code).SetError(truncRunes(ge.Message, 500))
	}
	if ge != nil && ge.Usage != nil {
		u.SetPromptTokens(ge.Usage.PromptTokens).SetCompletionTokens(ge.Usage.CompletionTokens).SetUsageEstimated(ge.Usage.Estimated)
	}
	if err := u.Exec(bg); err != nil {
		h.ch.log.Error("save assistant message failed", "conversation", cv.ID, "err", err)
	}
	_ = h.db.Conversation.UpdateOneID(cv.ID).SetUpdatedAt(time.Now()).Exec(bg)
	outcome := "ok"
	if rerr != nil || ge != nil || ter != nil {
		outcome = "error"
	}
	if awaiting {
		outcome = "awaiting_approval"
	}
	if stopped {
		outcome = "stopped"
	}
	detail := map[string]any{"message_id": asst.ID, "model": used.Model.Name,
		"fallback_from": fallbackFrom(used, tgt), "chars": utf8.RuneCountInString(buf.String()),
		"files": len(files), "context_truncated": ctxCut, "history_truncated": histCut,
		"history_dropped": histDropped, "est_prompt_tokens": estTokens}
	if len(tools) > 0 || len(st.Calls) > 0 {
		detail["tools"], detail["tool_steps"], detail["tool_calls"] = len(tools), st.Steps, len(st.Calls)
	}
	for k, v := range kbInfo {
		detail[k] = v
	}
	for k, v := range extra {
		detail[k] = v
	}
	mergeAgentAudit(detail, sess)
	errCode := ""
	if ge != nil {
		errCode = ge.Code
	}
	if ter != nil && toolAuditErr(ter) {
		errCode = "audit_failed"
	} else if ter != nil {
		errCode = "tool_failed"
	}
	if rerr != nil {
		errCode = "audit_failed"
	}
	h.finishAgentRun(bg, sess, st, usage, outcome, errCode)
	if err := audit.Record(c, audit.Entry{Action: action, Phase: "result", Outcome: outcome, ResourceType: "conversation",
		ResourceID: strconv.Itoa(cv.ID), Detail: detail}); err != nil {
		h.ch.log.Error("chat result audit write failed", "conversation", cv.ID)
	}
	if awaiting {
		sse(c, EventDone, view(string(message.StatusAwaitingApproval), used, tgt))
		return nil
	}
	if ter != nil {
		code, msg := "tool_failed", "工具调用处理失败"
		if toolAuditErr(ter) {
			code, msg = "audit_failed", toolAuditMsg
		}
		if c.Response().Committed {
			sse(c, EventError, map[string]any{"error": map[string]string{"code": code, "message": msg}})
			return nil
		}
		if toolAuditErr(ter) {
			return audit.ErrRejected
		}
		return ter
	}
	if rerr != nil {
		if c.Response().Committed {
			sse(c, EventError, map[string]any{"error": map[string]string{"code": "audit_failed", "message": "审计写入失败，未调用模型"}})
			return nil
		}
		return rerr
	}
	if ge != nil && !started && !stopped {
		return gwError(c, ge)
	}
	return nil
}

func textMessage(role, s string) gateway.Message {
	b, _ := json.Marshal(s)
	return gateway.Message{Role: role, Content: b}
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// allowed 解析模型并校验调用人的使用权限（无权限与不存在一律 404，D-32）。name 为空时用默认模型。
func (h *chatHandlers) allowed(c echo.Context, p *auth.Principal, name string) (*gateway.Target, error) {
	ctx := c.Request().Context()
	tgt, gerr := h.gw.Resolve(ctx, name)
	if gerr != nil {
		if gerr.Code == gateway.CodeModelNotFound {
			return nil, authz.Deny(c, "model:not_found")
		}
		return nil, &echo.HTTPError{Code: gerr.Status, Message: map[string]any{"error": gerr}}
	}
	if !p.Perms.Has(rbac.ModelManage) {
		ok, err := h.az.Check(ctx, p, rbac.ResModel, strconv.Itoa(tgt.Model.ID), rbac.BitUse)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, authz.Deny(c, "model:acl")
		}
	}
	return tgt, nil
}

// kbEmptyPrompt 选了知识库但没有可用资料（无权限、未就绪或没有命中）时的系统提示。
const kbEmptyPrompt = "用户为本次对话选择了知识库，但其中没有与问题相关、且用户有权访问的资料。请先明确告诉用户「没有可用资料」，" +
	"再视情况基于通用知识回答，并说明该回答不来自知识库。"

// kbContext 把检索结果编号注入系统提示，要求模型用 [n] 角标引用。
func kbContext(cs []kb.Citation) string {
	var b strings.Builder
	b.WriteString("以下是从用户所选知识库中检索到的资料，按编号列出。回答时优先依据这些资料，引用处用 [编号] 标注（如 [1]、[2]）；" +
		"资料不足以回答时如实说明，不要编造出处。\n")
	for _, ci := range cs {
		b.WriteString("\n[" + strconv.Itoa(ci.Index) + "] 《" + ci.FileName + "》")
		if ci.Heading != "" {
			b.WriteString(" · " + ci.Heading)
		}
		b.WriteString("\n" + ci.Content + "\n")
	}
	return b.String()
}
