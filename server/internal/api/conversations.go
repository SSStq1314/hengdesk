package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/conversation"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
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
	db *ent.Client
	ch *chatHandlers
}

type convView struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	ModelID   *int      `json:"model_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
}

func toConv(c *ent.Conversation) convView {
	return convView{ID: c.ID, Title: c.Title, ModelID: c.ModelID, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

func toMsg(m *ent.Message) msgView {
	return msgView{ID: m.ID, Role: string(m.Role), Content: m.Content, Status: string(m.Status), ModelName: m.ModelName,
		FallbackFrom: m.FallbackFrom, ErrorCode: m.ErrorCode, Error: m.Error, PromptTokens: m.PromptTokens,
		CompletionTokens: m.CompletionTokens, CreatedAt: m.CreatedAt}
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
	if in.Model != "" {
		tgt, err := h.ch.allowed(c, p, in.Model)
		if err != nil {
			return err
		}
		q.SetModelID(tgt.Model.ID)
	}
	cv, err := q.Save(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toConv(cv))
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
	ms, err := h.db.Message.Query().Where(message.ConversationID(cv.ID)).Order(ent.Asc(message.FieldID)).All(c.Request().Context())
	if err != nil {
		return err
	}
	out := make([]msgView, len(ms))
	for i, m := range ms {
		out[i] = toMsg(m)
	}
	return c.JSON(http.StatusOK, map[string]any{"conversation": toConv(cv), "messages": out})
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
	if in.Model != "" {
		tgt, err := h.ch.allowed(c, p, in.Model)
		if err != nil {
			return err
		}
		u.SetModelID(tgt.Model.ID)
	}
	cv, err = u.Save(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toConv(cv))
}

// remove 软删除：用户侧不可见（之后访问 404），消息保留供审计链路还原；物理清理随保留期策略（待产品确认）。
func (h *convHandlers) remove(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	cv, err := h.own(c, p)
	if err != nil {
		return err
	}
	if err := cv.Update().SetDeletedAt(time.Now()).Exec(c.Request().Context()); err != nil {
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
	name := in.Model
	if name == "" && cv.ModelID != nil {
		if m, err := h.db.Model.Get(ctx, *cv.ModelID); err == nil {
			name = m.Name
		}
	}
	tgt, err := h.ch.allowed(c, p, name)
	if err != nil {
		return err
	}
	if err := budgetGate(c, h.ch.bs, h.db, tgt.Model.ID, tgt.Model.Name); err != nil || c.Response().Committed {
		return err
	}
	if err := audit.Record(c, audit.Entry{Action: "chat.message.send", Phase: "attempt", ResourceType: "conversation",
		ResourceID: strconv.Itoa(cv.ID), Detail: map[string]any{"model": tgt.Model.Name, "chars": utf8.RuneCountInString(in.Content)}}); err != nil {
		return err // fail-closed
	}

	hist, err := h.db.Message.Query().Where(message.ConversationID(cv.ID), message.ContentNEQ("")).
		Order(ent.Desc(message.FieldID)).Limit(historyLimit - 1).All(ctx)
	if err != nil {
		return err
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

	msgs := make([]gateway.Message, 0, len(hist)+1)
	for i := len(hist) - 1; i >= 0; i-- {
		msgs = append(msgs, textMessage(string(hist[i].Role), hist[i].Content))
	}
	msgs = append(msgs, textMessage("user", in.Content))
	req := gateway.ChatRequest{Model: tgt.Model.Name, Messages: msgs, Stream: true}

	var buf strings.Builder
	hk := streamHooks{delta: "token", tap: func(d gateway.Delta) { buf.WriteString(d.Content) },
		done: func(res *gateway.ChatResponse, used, primary *gateway.Target) any {
			v := msgView{ID: asst.ID, Role: "assistant", Content: buf.String(), Status: "done", ModelName: used.Model.Name,
				FallbackFrom: fallbackFrom(used, primary), PromptTokens: res.Usage.PromptTokens,
				CompletionTokens: res.Usage.CompletionTokens, CreatedAt: asst.CreatedAt}
			return v
		}}
	res, ge, started, used, rerr := h.ch.run(c, p, &req, tgt, hk)

	// 落库用 WithoutCancel：客户端停止 / 断开后仍要保存已生成部分。
	bg := context.WithoutCancel(ctx)
	u := h.db.Message.UpdateOneID(asst.ID).SetModelName(used.Model.Name).SetFallbackFrom(fallbackFrom(used, tgt)).SetContent(buf.String())
	stopped := errors.Is(ctx.Err(), context.Canceled)
	switch {
	case rerr != nil:
		u.SetStatus(message.StatusError).SetErrorCode("audit_failed").SetError("审计写入失败，未调用模型")
	case ge == nil && res != nil:
		u.SetStatus(message.StatusDone).SetPromptTokens(res.Usage.PromptTokens).SetCompletionTokens(res.Usage.CompletionTokens)
		if buf.Len() == 0 {
			u.SetContent(contentText(res.Message.Content))
		}
	case stopped:
		u.SetStatus(message.StatusStopped)
	default:
		u.SetStatus(message.StatusError).SetErrorCode(ge.Code).SetError(truncRunes(ge.Message, 500))
	}
	if ge != nil && ge.Usage != nil {
		u.SetPromptTokens(ge.Usage.PromptTokens).SetCompletionTokens(ge.Usage.CompletionTokens)
	}
	if err := u.Exec(bg); err != nil {
		h.ch.log.Error("save assistant message failed", "conversation", cv.ID, "err", err)
	}
	_ = h.db.Conversation.UpdateOneID(cv.ID).SetUpdatedAt(time.Now()).Exec(bg)
	outcome := "ok"
	if rerr != nil || ge != nil {
		outcome = "error"
	}
	if stopped {
		outcome = "stopped"
	}
	if err := audit.Record(c, audit.Entry{Action: "chat.message.send", Phase: "result", Outcome: outcome, ResourceType: "conversation",
		ResourceID: strconv.Itoa(cv.ID), Detail: map[string]any{"message_id": asst.ID, "model": used.Model.Name,
			"fallback_from": fallbackFrom(used, tgt), "chars": utf8.RuneCountInString(buf.String())}}); err != nil {
		h.ch.log.Error("chat result audit write failed", "conversation", cv.ID)
	}
	if rerr != nil {
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
