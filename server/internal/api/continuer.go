package api

// 审批有结果后的自动续写（D-44 第 15、16 条，AC-08）。
//
// 审批通过 / 驳回 / 超时 / 撤回后，approval.Service.OnResolved 投递 asynq 任务 approval:continue（载荷为挂起的消息 ID），
// worker 用 Continuer 以发起人身份跑与 POST .../messages/:mid/continue 完全相同的逻辑（同一把会话锁、同样的审计与预算），
// 结果落库后前端轮询会话即可看到；并给发起人发站内提醒。
//
// 幂等与并发：会话锁（Valkey SET NX）保证同一会话不会并发续写；awaiting_approval → streaming 的条件更新保证只续写一次；
// 消息已不在等待状态、或审批仍未结束时任务直接结束。锁被占用时返回错误，由 asynq 稍后重试。
// 自动续写失败（模型不可用、预算超限等）时在消息上标记 error_code=auto_continue_failed，前端此时才显示「继续」按钮。
//
// 重启恢复：续写过程中工具每执行一步都把状态写入 tool_state（检查点）；进程崩溃后消息停在 streaming 且锁已过期，
// 定时补投发现后把它退回 awaiting_approval 再续写。已执行完的工具结果来自检查点或审批单，执行到一半的调用按「结果未知」交回模型，
// 不会重复执行。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/approval"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	entapproval "github.com/SSStq1314/hengdesk/server/internal/ent/approval"
	"github.com/SSStq1314/hengdesk/server/internal/ent/conversation"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
	"github.com/SSStq1314/hengdesk/server/internal/ent/notification"
)

// AutoContinueFailed 自动续写失败时消息上的 error_code（消息仍是 awaiting_approval，前端显示「继续」）。
const AutoContinueFailed = "auto_continue_failed"

// ErrContinueBusy 会话正在生成（锁被占用），稍后重试。
var ErrContinueBusy = errors.New("conversation busy")

// Continuer worker 侧的自动续写。
type Continuer struct {
	h    *convHandlers
	e    *echo.Echo
	db   *ent.Client
	aw   audit.Writer
	sp   *approval.Service
	lgOK bool
}

// NewContinuer 用与 HTTP 服务相同的依赖构造续写器（内部挂载一套不对外监听的路由以复用生成逻辑）。
func NewContinuer(d Deps) *Continuer {
	e := echo.New()
	h := mount(e, d)
	return &Continuer{h: h, e: e, db: d.DB, aw: d.Audit, sp: h.ap, lgOK: d.Log != nil}
}

// Approvals 续写器内部使用的审批服务（worker 用它处理超时与执行中断，结果同样会投递续写）。
func (x *Continuer) Approvals() *approval.Service { return x.sp }

func (x *Continuer) warn(msg string, args ...any) {
	if x.lgOK {
		x.h.ch.log.Warn(msg, args...)
	}
}

// Continue 续写一条挂起的消息。返回 ErrContinueBusy 或其他非业务错误时应重试；业务性失败已落库为 auto_continue_failed，返回 nil。
func (x *Continuer) Continue(ctx context.Context, msgID int) error {
	m, err := x.db.Message.Get(ctx, msgID)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if m.Status == message.StatusStreaming && len(m.ToolState) > 0 {
		recovered, err := x.recoverOrphan(ctx, m)
		if err != nil || !recovered {
			return err
		}
		m.Status = message.StatusAwaitingApproval
	}
	if m.Status != message.StatusAwaitingApproval || m.SupersededAt != nil {
		return nil
	}
	cv, err := x.db.Conversation.Query().Where(conversation.ID(m.ConversationID), conversation.DeletedAtIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	ap := x.pendingApproval(ctx, m)
	if ap != nil && (ap.Status == entapproval.StatusPending || (ap.Status == entapproval.StatusApproved && ap.ExecStatus == entapproval.ExecStatusRunning)) {
		return nil // 仍在审批 / 执行中：等它结束后会再投递
	}
	p, err := auth.LoadPrincipal(ctx, x.db, cv.UserID)
	if ent.IsNotFound(err) {
		return x.fail(ctx, m, cv, ap, "发起人账号已停用，未继续生成")
	}
	if err != nil {
		return err
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/approval-continue", nil).WithContext(ctx)
	req.Header.Set(echo.HeaderXRequestID, fmt.Sprintf("approval-continue-%d", m.ID))
	rec := httptest.NewRecorder()
	c := x.e.NewContext(req, rec)
	c.SetPath("/api/v1/conversations/:id/messages/:mid/continue")
	c.SetParamNames("id", "mid")
	c.SetParamValues(strconv.Itoa(cv.ID), strconv.Itoa(m.ID))
	c.Response().Header().Set(echo.HeaderXRequestID, req.Header.Get(echo.HeaderXRequestID))
	auth.SetCurrent(c, p)
	herr := audit.WithWriter(x.aw, auth.Actor)(x.h.continueMsg)(c)

	var he *echo.HTTPError
	if errors.As(herr, &he) && he.Code == http.StatusConflict {
		msg := fmt.Sprint(he.Message)
		switch msg {
		case "该会话正在生成回复，请等待完成或先停止":
			return ErrContinueBusy
		case "该回复不在等待审批状态", "工具调用仍在等待审批":
			return nil
		}
	}
	if herr != nil {
		if errors.As(herr, &he) {
			return x.fail(ctx, m, cv, ap, fmt.Sprint(he.Message))
		}
		if errors.Is(herr, audit.ErrRejected) {
			return x.fail(ctx, m, cv, ap, "审计写入失败，未继续生成")
		}
		return herr
	}
	if rec.Code >= 400 {
		// 预算拦截等处理器直接写了 JSON 错误响应（形如 {"error":{"code","message"}}）。
		return x.fail(ctx, m, cv, ap, parseHTTPErrorMessage(rec.Body.Bytes(), rec.Code))
	}
	x.notify(ctx, cv, m, ap, "")
	return nil
}

// parseHTTPErrorMessage 解析 API 错误体。预算拦截是 {"error":{"message":"本月部门预算已用尽…"}}，
// 不能只读顶层 message，否则会退化成 HTTP 状态文本（「Too Many Requests」）。
func parseHTTPErrorMessage(body []byte, status int) string {
	var wrap struct {
		Message string `json:"message"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &wrap)
	if wrap.Error.Message != "" {
		return wrap.Error.Message
	}
	if wrap.Message != "" {
		return wrap.Message
	}
	return http.StatusText(status)
}

// recoverOrphan 续写 / 生成中进程崩溃：消息停在 streaming 且会话锁已过期 → 退回 awaiting_approval（只做一次，条件更新）。
func (x *Continuer) recoverOrphan(ctx context.Context, m *ent.Message) (bool, error) {
	if x.h.r == nil {
		return false, nil
	}
	n, err := x.h.r.Exists(ctx, "hengdesk:chat:gen:"+strconv.Itoa(m.ConversationID)).Result()
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil // 仍有进程在生成
	}
	k, err := x.db.Message.Update().Where(message.ID(m.ID), message.StatusEQ(message.StatusStreaming)).
		SetStatus(message.StatusAwaitingApproval).Save(ctx)
	if err != nil {
		return false, err
	}
	if k == 1 {
		x.warn("recovered interrupted generation", "message", m.ID, "conversation", m.ConversationID)
	}
	return k == 1, nil
}

// pendingApproval 挂起点（队首调用）对应的审批单；没有时返回 nil。
func (x *Continuer) pendingApproval(ctx context.Context, m *ent.Message) *ent.Approval {
	var st toolState
	if json.Unmarshal(m.ToolState, &st) != nil || len(st.Queue) == 0 {
		return nil
	}
	for _, v := range st.Calls {
		if v.ID == st.Queue[0].ID && v.ApprovalID != 0 {
			ap, err := x.db.Approval.Get(ctx, v.ApprovalID)
			if err == nil {
				return ap
			}
		}
	}
	return nil
}

// fail 标记自动续写失败（消息保持 awaiting_approval，前端显示「继续」）并提醒发起人。
func (x *Continuer) fail(ctx context.Context, m *ent.Message, cv *ent.Conversation, ap *ent.Approval, reason string) error {
	x.warn("auto continue failed", "message", m.ID, "reason", reason)
	reason = truncRunes(reason, 400)
	if err := x.db.Message.Update().Where(message.ID(m.ID), message.StatusEQ(message.StatusAwaitingApproval)).
		SetErrorCode(AutoContinueFailed).SetError(reason).Exec(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	x.notify(ctx, cv, m, ap, reason)
	return nil
}

// MarkFailed 重试次数用尽时由 worker 调用。
func (x *Continuer) MarkFailed(ctx context.Context, msgID int, reason string) error {
	m, err := x.db.Message.Get(ctx, msgID)
	if err != nil || m.Status != message.StatusAwaitingApproval {
		return nil
	}
	cv, err := x.db.Conversation.Get(ctx, m.ConversationID)
	if err != nil {
		return nil
	}
	return x.fail(ctx, m, cv, x.pendingApproval(ctx, m), reason)
}

var resolvedText = map[entapproval.Status]string{
	entapproval.StatusApproved:  "已通过",
	entapproval.StatusRejected:  "被驳回",
	entapproval.StatusExpired:   "已超时",
	entapproval.StatusWithdrawn: "已撤回",
}

// notify 给发起人发站内提醒（同一审批单 / 同一结果只提醒一次）。
func (x *Continuer) notify(ctx context.Context, cv *ent.Conversation, m *ent.Message, ap *ent.Approval, failReason string) {
	title := "回复已自动继续生成"
	key := fmt.Sprintf("approval-continue:m%d", m.ID)
	detail := map[string]any{"conversation_id": cv.ID, "message_id": m.ID}
	if ap != nil {
		title = "工具「" + ap.ToolName + "」审批" + resolvedText[ap.Status] + "，回复已自动继续生成"
		key = fmt.Sprintf("approval-continue:%d", ap.ID)
		detail["approval_id"] = ap.ID
		detail["status"] = string(ap.Status)
	}
	if failReason != "" {
		title = "回复未能自动继续：" + failReason + "。请打开会话点「继续」"
		key += ":failed"
		detail["error"] = failReason
	}
	bg := context.WithoutCancel(ctx)
	ex, err := x.db.Notification.Query().Where(notification.UserID(cv.UserID), notification.DedupeKey(key)).Exist(bg)
	if err != nil || ex {
		return
	}
	if err := x.db.Notification.Create().SetOrgID(cv.OrgID).SetUserID(cv.UserID).SetKind(approval.NotifyKind).
		SetTitle(truncRunes(title, 200)).SetDetail(detail).SetDedupeKey(key).Exec(bg); err != nil {
		x.warn("continue notification failed", "message", m.ID, "err", err)
	}
}

// Sweep 定时补投：
//   - 审批执行停在 running 太久（执行中崩溃）→ 按结果未知落库；
//   - 挂起消息的审批已有结果但没有续写（投递丢失 / worker 重启）且未标记失败 → 续写；
//   - 消息停在 streaming、带工具检查点且会话锁已过期（续写中崩溃）→ 续写。
//
// enqueue 投递续写任务；返回投递数量。
func (x *Continuer) Sweep(ctx context.Context, enqueue func(ctx context.Context, messageID int) error) (int, error) {
	if _, err := x.sp.RecoverStuck(ctx); err != nil {
		return 0, err
	}
	ms, err := x.db.Message.Query().Where(message.StatusIn(message.StatusAwaitingApproval, message.StatusStreaming),
		message.SupersededAtIsNil(), message.ToolStateNotNil()).Limit(500).All(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range ms {
		switch m.Status {
		case message.StatusAwaitingApproval:
			if m.ErrorCode == AutoContinueFailed {
				continue
			}
			ap := x.pendingApproval(ctx, m)
			if ap == nil || ap.Status == entapproval.StatusPending ||
				(ap.Status == entapproval.StatusApproved && ap.ExecStatus == entapproval.ExecStatusRunning) {
				continue
			}
		case message.StatusStreaming:
			if x.h.r == nil {
				continue
			}
			k, err := x.h.r.Exists(ctx, "hengdesk:chat:gen:"+strconv.Itoa(m.ConversationID)).Result()
			if err != nil || k > 0 {
				continue
			}
		}
		if err := enqueue(ctx, m.ID); err != nil {
			x.warn("sweep enqueue continue failed", "message", m.ID, "err", err)
			continue
		}
		n++
	}
	return n, nil
}
