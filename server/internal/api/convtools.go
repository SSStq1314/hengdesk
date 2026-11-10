package api

// 会话里的工具调用（M1 第五块 PR4，FR-23/25）。
//
// 流程：会话选了工具时，生成请求带上当前仍可用的工具（每次按 authz.ToolAccess 现算）。模型提出工具调用后逐个处理：
//   - 工具已不可用 / 不存在 / 参数不是 JSON：不执行，把原因作为工具结果交回模型；
//   - 自动执行（auto）：approval.Service.Execute（tool.call attempt 审计写失败则不执行，整条回复以 audit_failed 结束）；
//   - 需要审批（require_approval）：生成审批单（审计写失败同样不生成、不执行），消息挂起为 awaiting_approval，
//     工具循环状态落库（tool_state），审批结束后由 POST .../messages/:mid/continue 接着跑，进程重启不影响。
// 一条回复最多 maxToolSteps 轮工具调用；到上限后用 tool_choice=none 让模型直接作答，并提示用户。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/approval"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	entapproval "github.com/SSStq1314/hengdesk/server/internal/ent/approval"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
	"github.com/SSStq1314/hengdesk/server/internal/ent/tool"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
)

const (
	maxToolSteps    = 5
	maxConvTools    = 20
	toolResultRunes = 16000
	toolStepsNotice = "工具调用已达 5 步上限，已停止继续调用工具"
	// errToolAudit 工具调用 / 审批单的审计写不进去：不执行工具，本条回复以 audit_failed 结束。
	toolAuditMsg = "审计写入失败，未执行工具"
)

// toolState 挂起（等待审批）时落库的工具循环状态。
type toolState struct {
	// Trace 已发生的工具轮次：带 tool_calls 的 assistant 消息与对应 tool 结果，按顺序原样回传模型。
	Trace []gateway.Message `json:"trace"`
	// Queue 最后一轮中尚未交回结果的调用（从等待审批的那个开始）。
	Queue []gateway.ToolCall `json:"queue,omitempty"`
	Steps int                `json:"steps"`
	Calls []ToolCall         `json:"calls"`
	// Content 挂起前已经生成的正文。
	Content string `json:"content,omitempty"`
}

var toolNameRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// fnName 模型侧函数名：只允许 [A-Za-z0-9_-]，最长 64；重名时追加工具 ID。
func fnName(t *ent.Tool, used map[string]bool) string {
	n := toolNameRe.ReplaceAllString(t.Name, "_")
	if n == "" {
		n = "tool"
	}
	if len(n) > 56 {
		n = n[:56]
	}
	if used[n] {
		n = fmt.Sprintf("%s_%d", n, t.ID)
	}
	used[n] = true
	return n
}

// checkTools 校验会话选用的工具：必须当前可用（authz.ToolAccess），否则与不存在一致 404。
func (h *convHandlers) checkTools(c echo.Context, p *auth.Principal, ids []int) ([]int, error) {
	if h.az == nil {
		return nil, echo.ErrNotFound
	}
	if len(ids) > maxConvTools {
		return nil, bad("最多选择 " + strconv.Itoa(maxConvTools) + " 个工具")
	}
	ctx := c.Request().Context()
	seen := map[int]bool{}
	out := []int{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		t, err := h.db.Tool.Query().Where(tool.ID(id), tool.OrgID(p.OrgID)).Only(ctx)
		if ent.IsNotFound(err) {
			return nil, authz.Deny(c, "tool:not_found")
		}
		if err != nil {
			return nil, err
		}
		d, err := h.az.ToolAccess(ctx, p, t)
		if err != nil {
			return nil, err
		}
		if !d.Usable {
			return nil, authz.Deny(c, "tool:not_usable")
		}
		out = append(out, id)
	}
	return out, nil
}

// convTools 本次生成提供给模型的工具（选用且当前仍可用的），以及函数名到工具的映射。
func (h *convHandlers) convTools(ctx context.Context, p *auth.Principal, cv *ent.Conversation) ([]gateway.Tool, map[string]*ent.Tool, error) {
	tm := map[string]*ent.Tool{}
	if len(cv.ToolIds) == 0 || h.az == nil || h.ap == nil {
		return nil, tm, nil
	}
	ts, err := h.db.Tool.Query().Where(tool.IDIn(cv.ToolIds...), tool.OrgID(p.OrgID)).Order(ent.Asc(tool.FieldID)).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	used := map[string]bool{}
	var out []gateway.Tool
	for _, t := range ts {
		d, err := h.az.ToolAccess(ctx, p, t)
		if err != nil {
			return nil, nil, err
		}
		if !d.Usable {
			continue
		}
		n := fnName(t, used)
		params := t.InputSchema
		if len(params) == 0 {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		fn, _ := json.Marshal(map[string]any{"name": n, "description": truncRunes(t.Description, 1000), "parameters": params})
		out = append(out, gateway.Tool{Type: "function", Function: fn})
		tm[n] = t
	}
	return out, tm, nil
}

// sse 在对话流里发一个事件（必要时先写响应头）。
func sse(c echo.Context, event string, v any) {
	w := c.Response()
	if !w.Committed {
		w.Header().Set(echo.HeaderContentType, "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, mustJSON(v))
	w.Flush()
}

func argsSummary(args map[string]any) string {
	b, _ := json.Marshal(approval.Redact(args))
	return truncRunes(string(b), 200)
}

func toolMessage(id string, r approval.Result) gateway.Message {
	txt := truncRunes(r.Text, toolResultRunes)
	if r.IsError {
		txt = "[工具调用失败] " + txt
	}
	m := textMessage("tool", txt)
	m.ToolCallID = id
	return m
}

func callView(st *toolState, tc gateway.ToolCall) *ToolCall {
	for i := range st.Calls {
		if st.Calls[i].ID == tc.ID {
			return &st.Calls[i]
		}
	}
	st.Calls = append(st.Calls, ToolCall{ID: tc.ID, Tool: tc.Function.Name, Status: ToolCallPending})
	return &st.Calls[len(st.Calls)-1]
}

// runTools 依次处理 st.Queue 中的调用。遇到需要审批（或仍在审批中）的调用时停下，返回 awaiting=true，
// Queue 保留从它开始的剩余调用。返回 approval.ErrAudit 表示审计写失败（未执行）。
func (h *convHandlers) runTools(c echo.Context, p *auth.Principal, cv *ent.Conversation, asst *ent.Message,
	tm map[string]*ent.Tool, st *toolState, summary string) (bool, error) {
	ctx := c.Request().Context()
	a := actorOf(p)
	for i, tc := range st.Queue {
		v := callView(st, tc)
		var res approval.Result
		if v.ApprovalID != 0 {
			ap, err := h.ap.Get(ctx, a, v.ApprovalID)
			if err != nil {
				return false, err
			}
			if ap.Status == entapproval.StatusApproved && ap.ExecStatus == entapproval.ExecStatusNone {
				// 审批人通过后执行时审计失败等情况：由发起人继续时再执行一次（仍然 exactly-once）。
				if ap, err = h.ap.RunApproved(ctx, ap); err != nil {
					return false, err
				}
			}
			r, ok := approval.ModelResult(ap)
			if !ok {
				st.Queue = st.Queue[i:]
				return true, nil
			}
			res = r
			switch {
			case ap.Status != entapproval.StatusApproved:
				v.Status, v.ErrorCode = ToolCallRejected, string(ap.Status)
			case r.IsError:
				v.Status = ToolCallFailed
			default:
				v.Status = ToolCallSucceeded
			}
		} else {
			t := tm[tc.Function.Name]
			var args map[string]any
			switch {
			case t == nil:
				res, v.Status, v.ErrorCode = approval.Result{Text: "该工具不存在或当前不可用，未执行。", IsError: true}, ToolCallRejected, "unavailable"
			case json.Unmarshal([]byte(nonEmptyArgs(tc.Function.Arguments)), &args) != nil:
				res, v.Status, v.ErrorCode = approval.Result{Text: "工具参数不是合法的 JSON 对象，未执行。", IsError: true}, ToolCallFailed, "bad_args"
			}
			if t != nil {
				v.Tool = t.Name
				if t.ServerID != nil {
					v.ServerID = *t.ServerID
				}
			}
			if v.Status == ToolCallPending {
				if args == nil {
					args = map[string]any{}
				}
				v.ArgsSummary = argsSummary(args)
				// 执行前按当前策略与授权重新判定（选工具之后可能被禁用或收回授权）。
				d, err := h.az.ToolAccess(ctx, p, t)
				if err != nil {
					return false, err
				}
				switch {
				case !d.Usable:
					res, v.Status, v.ErrorCode = approval.Result{Text: "该工具当前不可用，未执行。", IsError: true}, ToolCallRejected, "unavailable"
				case d.Mode == "auto":
					srv, _, err := h.ap.Usable(ctx, p.OrgID, t)
					if err != nil {
						return false, err
					}
					v.Status = ToolCallRunning
					sse(c, EventToolCall, v)
					if res, err = h.ap.Execute(ctx, a, t, srv, args, 0); err != nil {
						v.Status, v.ErrorCode = ToolCallFailed, "audit_failed"
						sse(c, EventToolCall, v)
						return false, err
					}
					v.Status = ToolCallSucceeded
					if res.IsError {
						v.Status = ToolCallFailed
					}
				default:
					ap, err := h.ap.Create(ctx, a, approval.CreateInput{Tool: t, Args: args, ConversationID: &cv.ID, MessageID: &asst.ID,
						ToolCallID: tc.ID, ContextSummary: summary})
					if err != nil {
						return false, err
					}
					v.Status, v.ApprovalID = ToolCallApproval, ap.ID
					sse(c, EventToolCall, v)
					sse(c, EventApprovalPending, ApprovalPending{ApprovalID: ap.ID, ToolCallID: tc.ID, Tool: t.Name,
						Reason: "工具「" + t.Name + "」需要审批后才能执行"})
					st.Queue = st.Queue[i:]
					return true, nil
				}
			}
		}
		v.ResultSummary = truncRunes(res.Text, 200)
		st.Trace = append(st.Trace, toolMessage(tc.ID, res))
		sse(c, EventToolCall, v)
	}
	st.Queue = nil
	return false, nil
}

func nonEmptyArgs(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	return s
}

// continueMsg POST /conversations/:id/messages/:mid/continue：审批结束（通过 / 拒绝 / 超时 / 撤回）后继续生成。
// 只有会话本人可调用；消息必须是最后一条且处于 awaiting_approval；仍有审批未结束时 409。
func (h *convHandlers) continueMsg(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	cv, err := h.own(c, p)
	if err != nil {
		return err
	}
	mid, err := strconv.Atoi(c.Param("mid"))
	if err != nil {
		return authz.Deny(c, "message:not_found")
	}
	ctx := c.Request().Context()
	unlock, err := h.lock(ctx, cv.ID)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := h.db.Message.Query().Where(message.ID(mid), message.ConversationID(cv.ID), message.SupersededAtIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return authz.Deny(c, "message:not_found")
	}
	if err != nil {
		return err
	}
	if m.Status != message.StatusAwaitingApproval || h.ap == nil {
		return echo.NewHTTPError(http.StatusConflict, "该回复不在等待审批状态")
	}
	var st toolState
	if err := json.Unmarshal(m.ToolState, &st); err != nil {
		return echo.NewHTTPError(http.StatusConflict, "工具调用状态已损坏，请重新生成")
	}
	// 仍在审批中：直接 409，不调用模型。
	a := actorOf(p)
	if len(st.Queue) > 0 {
		if v := callView(&st, st.Queue[0]); v.ApprovalID != 0 {
			ap, err := h.ap.Get(ctx, a, v.ApprovalID)
			if err != nil {
				return err
			}
			if ap.Status == entapproval.StatusPending || ap.ExecStatus == entapproval.ExecStatusRunning {
				return echo.NewHTTPError(http.StatusConflict, "工具调用仍在等待审批")
			}
		}
	}
	sess, err := h.resolveAgent(c, p, cv)
	if err != nil {
		return err
	}
	var tgt *gateway.Target
	var notice string
	if sess != nil {
		tgt, notice, err = h.pickAgentModel(c, p, sess, m.ModelName)
		if err != nil {
			tgt, notice, err = h.pickAgentModel(c, p, sess, "")
		}
	} else {
		tgt, notice, err = h.pickModel(c, p, cv, m.ModelName)
		if err != nil {
			tgt, notice, err = h.pickModel(c, p, cv, "")
		}
	}
	if err != nil {
		return err
	}
	if err := budgetGate(c, h.ch.bs, h.db, tgt.Model.ID, tgt.Model.Name); err != nil || c.Response().Committed {
		return err
	}
	if err := audit.Record(c, audit.Entry{Action: "chat.message.continue", Phase: "attempt", ResourceType: "conversation",
		ResourceID: strconv.Itoa(cv.ID), Detail: map[string]any{"model": tgt.Model.Name, "message_id": m.ID, "steps": st.Steps}}); err != nil {
		return err // fail-closed
	}
	n, err := h.db.Message.Update().Where(message.ID(m.ID), message.StatusEQ(message.StatusAwaitingApproval)).
		SetStatus(message.StatusStreaming).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return echo.NewHTTPError(http.StatusConflict, "该回复不在等待审批状态")
	}
	return h.generateWith(c, p, cv, tgt, m, "chat.message.continue", notice, false, map[string]any{"continued": true}, &st)
}

// awaitingLast 会话最后一条回复是否在等待审批（此时不允许发送新消息或重新生成，避免审批结果无处交回）。
func (h *convHandlers) awaitingLast(ctx context.Context, convID int) (bool, error) {
	return h.db.Message.Query().Where(message.ConversationID(convID), message.SupersededAtIsNil(),
		message.StatusEQ(message.StatusAwaitingApproval)).Exist(ctx)
}

var errAwaiting = echo.NewHTTPError(http.StatusConflict, "上一条回复正在等待工具审批，请等审批结束后继续，或先撤回审批")

func toolAuditErr(err error) bool { return errors.Is(err, approval.ErrAudit) }
