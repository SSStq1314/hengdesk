package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/docparse"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/conversation"
	"github.com/SSStq1314/hengdesk/server/internal/ent/conversationfile"
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
	db     *ent.Client
	ch     *chatHandlers
	fs     FileStore
	lim    FileLimits
	parser *docparse.Extractor
	r      redis.UniversalClient
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
	// ContextTruncated 仅出现在 done 事件：本轮会话文件内容因过长被截断（UI 提示用户）。
	ContextTruncated bool `json:"context_truncated,omitempty"`
	// HistoryTruncated 仅出现在 done 事件：较早的历史消息因超出模型上下文窗口未发送（D-40 第 4 条）。
	HistoryTruncated bool `json:"history_truncated,omitempty"`
	// Notice 仅出现在 done 事件：记住的模型已不可用、已回退默认模型等需要告知用户的提示（D-40 第 8 条）。
	Notice string `json:"notice,omitempty"`
	// UsageEstimated 上游未返回 usage，token 为估算值（D-41 第 4 条，界面标「估算」）。
	UsageEstimated bool `json:"usage_estimated,omitempty"`
}

func toConv(c *ent.Conversation) convView {
	return convView{ID: c.ID, Title: c.Title, ModelID: c.ModelID, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

func toMsg(m *ent.Message) msgView {
	return msgView{ID: m.ID, Role: string(m.Role), Content: m.Content, Status: string(m.Status), ModelName: m.ModelName,
		FallbackFrom: m.FallbackFrom, ErrorCode: m.ErrorCode, Error: m.Error, PromptTokens: m.PromptTokens, UsageEstimated: m.UsageEstimated,
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
	ms, err := h.db.Message.Query().Where(message.ConversationID(cv.ID), message.SupersededAtIsNil()).Order(ent.Asc(message.FieldID)).All(c.Request().Context())
	if err != nil {
		return err
	}
	out := make([]msgView, len(ms))
	for i, m := range ms {
		out[i] = toMsg(m)
	}
	fs, err := h.files(c.Request().Context(), cv.ID)
	if err != nil {
		return err
	}
	fv := make([]fileView, len(fs))
	for i, f := range fs {
		fv[i] = toFile(f)
	}
	return c.JSON(http.StatusOK, map[string]any{"conversation": toConv(cv), "messages": out, "files": fv})
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
	tgt, notice, err := h.pickModel(c, p, cv, in.Model)
	if err != nil {
		return err
	}
	if err := budgetGate(c, h.ch.bs, h.db, tgt.Model.ID, tgt.Model.Name); err != nil || c.Response().Committed {
		return err
	}
	if err := audit.Record(c, audit.Entry{Action: "chat.message.send", Phase: "attempt", ResourceType: "conversation",
		ResourceID: strconv.Itoa(cv.ID), Detail: map[string]any{"model": tgt.Model.Name, "chars": utf8.RuneCountInString(in.Content),
			"model_reset": notice != ""}}); err != nil {
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
	return h.generate(c, p, cv, tgt, asst, "chat.message.send", notice, nil)
}

type regenInput struct {
	MessageID int `json:"message_id"`
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
	if last.Role != message.RoleAssistant || last.Status == message.StatusStreaming {
		return echo.NewHTTPError(http.StatusConflict, "只能重新生成最后一条已结束的回复")
	}
	tgt, notice, err := h.pickModel(c, p, cv, "")
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
			"replaces_message_id": last.ID, "model_reset": notice != ""}}); err != nil {
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
	return h.generate(c, p, cv, tgt, asst, "chat.message.regenerate", notice, map[string]any{"kind": kind, "replaces_message_id": last.ID})
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
	action, notice string, extra map[string]any) error {
	ctx := c.Request().Context()
	files, err := h.files(ctx, cv.ID)
	if err != nil {
		return err
	}
	hist, err := h.db.Message.Query().Where(message.ConversationID(cv.ID), message.SupersededAtIsNil(), message.ContentNEQ(""),
		message.IDLT(asst.ID)).Order(ent.Desc(message.FieldID)).Limit(historyLimit).All(ctx)
	if err != nil {
		return err
	}
	cms := make([]ctxMsg, 0, len(hist)+1)
	fctx, ctxCut := fileContext(files, h.lim.TotalChars)
	if fctx != "" {
		cms = append(cms, ctxMsg{Role: "system", Content: fctx})
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

	var buf strings.Builder
	hk := streamHooks{delta: "token", tap: func(d gateway.Delta) { buf.WriteString(d.Content) },
		done: func(res *gateway.ChatResponse, used, primary *gateway.Target) any {
			return msgView{ID: asst.ID, Role: "assistant", Content: buf.String(), Status: "done", ModelName: used.Model.Name,
				FallbackFrom: fallbackFrom(used, primary), PromptTokens: res.Usage.PromptTokens, UsageEstimated: res.Usage.Estimated,
				CompletionTokens: res.Usage.CompletionTokens, CreatedAt: asst.CreatedAt, ContextTruncated: ctxCut, HistoryTruncated: histCut, Notice: notice}
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
	if rerr != nil || ge != nil {
		outcome = "error"
	}
	if stopped {
		outcome = "stopped"
	}
	detail := map[string]any{"message_id": asst.ID, "model": used.Model.Name,
		"fallback_from": fallbackFrom(used, tgt), "chars": utf8.RuneCountInString(buf.String()),
		"files": len(files), "context_truncated": ctxCut, "history_truncated": histCut,
		"history_dropped": histDropped, "est_prompt_tokens": estTokens}
	for k, v := range extra {
		detail[k] = v
	}
	if err := audit.Record(c, audit.Entry{Action: action, Phase: "result", Outcome: outcome, ResourceType: "conversation",
		ResourceID: strconv.Itoa(cv.ID), Detail: detail}); err != nil {
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
