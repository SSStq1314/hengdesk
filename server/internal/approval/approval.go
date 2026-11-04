// Package approval 工具调用的执行与审批（FR-25 / FR-28，M1 第五块 PR3）。
//
// 规则：
//   - 每次执行先写 tool.call attempt 审计，写失败不执行（fail-closed）；参数在审计中脱敏并截断。
//   - 审批单保存参数快照，通过后用原参数执行恰好一次（exec_status 条件更新）。
//   - 超时一律按拒绝（D-27）：读取时实时判定，另由定时任务落库。
//   - 不可自批：发起人即使在审批人名单里也不能审批自己的单子。
package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/approval"
	"github.com/SSStq1314/hengdesk/server/internal/ent/mcpserver"
	"github.com/SSStq1314/hengdesk/server/internal/ent/tool"
	"github.com/SSStq1314/hengdesk/server/internal/policy"
)

// TaskExpire 定时把到期的待审批单落库为 expired。
const TaskExpire = "approval:expire"

// NotifyKind 站内提醒类型。
const NotifyKind = "approval"

// Caller 真正发起一次远程调用（API 层用 MCP 客户端实现）。
type Caller interface {
	Call(ctx context.Context, t *ent.Tool, s *ent.McpServer, args map[string]any) (text string, isError bool, err error)
}

// Actor 调用 / 审批的操作者。
type Actor struct {
	OrgID int
	ID    int
	Email string
	// CanManage 是否拥有 tool.manage（审批人为空时由这类管理员审批）。
	CanManage bool
}

// Service 执行与审批服务。
type Service struct {
	DB     *ent.Client
	Audit  audit.Writer
	Caller Caller
	Log    *slog.Logger
	Now    func() time.Time
}

var (
	// ErrNotFound 不存在或无权查看（API 统一 404）。
	ErrNotFound = errors.New("approval not found")
	// ErrSelfApproval 发起人不能审批自己的单子。
	ErrSelfApproval = errors.New("不能审批自己发起的申请")
	// ErrNotPending 单子已处理（通过 / 拒绝 / 超时 / 撤回）。
	ErrNotPending = errors.New("审批单已处理")
	// ErrAudit 审计写入失败，操作已拒绝。
	ErrAudit = errors.New("审计服务不可用，操作已拒绝")
	// ErrToolUnavailable 工具当前不可用（已停用 / 禁止 / 待复核 / 服务器停用）。
	ErrToolUnavailable = errors.New("工具当前不可用")
)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// ---------- 参数脱敏 ----------

var sensitiveKeys = []string{"password", "passwd", "secret", "token", "apikey", "api_key", "authorization", "cookie", "credential", "private_key", "access_key", "key"}

const maxAuditString = 500

// Redact 返回适合写入审计的参数副本：敏感键名的值替换为 ***，长字符串截断。
func Redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if isSensitive(k) {
				out[k] = "***"
				continue
			}
			out[k] = Redact(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = Redact(val)
		}
		return out
	case string:
		if len([]rune(x)) > maxAuditString {
			return string([]rune(x)[:maxAuditString]) + "…"
		}
		return x
	default:
		return v
	}
}

func isSensitive(k string) bool {
	l := strings.ToLower(strings.ReplaceAll(k, "-", "_"))
	for _, s := range sensitiveKeys {
		if s == "key" {
			if l == "key" || strings.HasSuffix(l, "_key") {
				return true
			}
			continue
		}
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// ArgsHash 参数快照哈希（规范化 JSON）。
func ArgsHash(args map[string]any) string {
	b, _ := json.Marshal(args) // map 键有序
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ---------- 执行 ----------

// Result 一次工具调用的结果（交回模型）。
type Result struct {
	Text    string `json:"text"`
	IsError bool   `json:"is_error"`
}

// Usable 重新按策略判定工具当前是否可用（不含 ACL，ACL 由调用方判断）。
func (s *Service) Usable(ctx context.Context, orgID int, t *ent.Tool) (*ent.McpServer, policy.Decision, error) {
	var srv *ent.McpServer
	if t.ServerID != nil {
		v, err := s.DB.McpServer.Query().Where(mcpserver.ID(*t.ServerID)).Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, policy.Decision{}, err
		}
		srv = v
	}
	return srv, policy.Decide(orgID, t, srv), nil
}

// Execute 执行一次工具调用：attempt 审计（失败不执行）→ 远程调用 → result 审计。
// approvalID 非 0 时表示经审批后执行。工具自身报错与连接错误都作为结果返回（IsError），只有审计失败返回 ErrAudit。
func (s *Service) Execute(ctx context.Context, a Actor, t *ent.Tool, srv *ent.McpServer, args map[string]any, approvalID int) (Result, error) {
	detail := map[string]any{"tool": t.Key, "tool_name": t.Name, "risk": string(t.RiskLevel), "args": Redact(args)}
	if approvalID != 0 {
		detail["approval_id"] = approvalID
	}
	e := audit.Entry{OrgID: a.OrgID, ActorID: a.ID, ActorEmail: a.Email, Action: "tool.call", ResourceType: "tool",
		ResourceID: fmt.Sprint(t.ID), Phase: "attempt", Detail: detail}
	if err := s.Audit.Write(ctx, e); err != nil {
		return Result{}, ErrAudit
	}
	text, isErr, err := s.Caller.Call(ctx, t, srv, args)
	res := Result{Text: text, IsError: isErr}
	outcome := "success"
	if err != nil {
		res = Result{Text: err.Error(), IsError: true}
		outcome = "error"
	} else if isErr {
		outcome = "tool_error"
	}
	e.Phase, e.Outcome = "result", outcome
	e.Detail = map[string]any{"tool": t.Key, "is_error": res.IsError, "result_len": len(res.Text)}
	if approvalID != 0 {
		e.Detail["approval_id"] = approvalID
	}
	if err := s.Audit.Write(context.WithoutCancel(ctx), e); err != nil {
		s.log().Error("tool.call result audit failed", "tool", t.Key, "err", err)
	}
	return res, nil
}

// ---------- 审批单 ----------

// CreateInput 发起审批所需信息。
type CreateInput struct {
	Tool           *ent.Tool
	Args           map[string]any
	ConversationID *int
	MessageID      *int
	ToolCallID     string
	ContextSummary string
}

// Create 生成待审批单、写审计并提醒审批人。审计写失败则不生成（fail-closed）。
func (s *Service) Create(ctx context.Context, a Actor, in CreateInput) (*ent.Approval, error) {
	t := in.Tool
	if in.Args == nil {
		in.Args = map[string]any{}
	}
	sum := []rune(in.ContextSummary)
	if len(sum) > 2000 {
		sum = sum[:2000]
	}
	approvers := slices.Clone(t.ApproverIds)
	if err := s.Audit.Write(ctx, audit.Entry{OrgID: a.OrgID, ActorID: a.ID, ActorEmail: a.Email, Action: "approval.create",
		ResourceType: "tool", ResourceID: fmt.Sprint(t.ID), Phase: "attempt",
		Detail: map[string]any{"tool": t.Key, "args": Redact(in.Args)}}); err != nil {
		return nil, ErrAudit
	}
	tx, err := s.DB.Tx(ctx)
	if err != nil {
		return nil, err
	}
	ap, err := tx.Approval.Create().SetOrgID(a.OrgID).SetToolID(t.ID).SetToolKey(t.Key).SetToolName(t.Name).
		SetRiskLevel(string(t.RiskLevel)).SetRequesterID(a.ID).SetNillableConversationID(in.ConversationID).
		SetNillableMessageID(in.MessageID).SetToolCallID(in.ToolCallID).SetArgs(in.Args).SetArgsHash(ArgsHash(in.Args)).
		SetContextSummary(string(sum)).SetApproverIds(approvers).
		SetExpiresAt(s.now().Add(time.Duration(t.ApprovalTimeoutSec) * time.Second)).
		SetTraceID(audit.TraceFrom(ctx)).Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	var recips []int
	for _, id := range approvers {
		if id != a.ID {
			recips = append(recips, id)
		}
	}
	if len(recips) > 0 {
		bulk := make([]*ent.NotificationCreate, 0, len(recips))
		for _, uid := range recips {
			bulk = append(bulk, tx.Notification.Create().SetOrgID(a.OrgID).SetUserID(uid).SetKind(NotifyKind).
				SetTitle("待审批：调用工具「"+t.Name+"」").
				SetDetail(map[string]any{"approval_id": ap.ID, "requester": a.Email}).
				SetDedupeKey(fmt.Sprintf("approval:%d", ap.ID)))
		}
		if err := tx.Notification.CreateBulk(bulk...).Exec(ctx); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.result(ctx, a, "approval.create", ap, "success", nil)
	return ap, nil
}

func (s *Service) result(ctx context.Context, a Actor, action string, ap *ent.Approval, outcome string, extra map[string]any) {
	d := map[string]any{"tool": ap.ToolKey, "approval_id": ap.ID, "status": string(ap.Status)}
	for k, v := range extra {
		d[k] = v
	}
	e := audit.Entry{OrgID: ap.OrgID, ActorID: a.ID, ActorEmail: a.Email, Action: action, ResourceType: "approval",
		ResourceID: fmt.Sprint(ap.ID), Phase: "result", Outcome: outcome, Detail: d, TraceID: ap.TraceID}
	if err := s.Audit.Write(context.WithoutCancel(ctx), e); err != nil {
		s.log().Error("approval audit failed", "action", action, "id", ap.ID, "err", err)
	}
}

// IsApprover a 是否可以审批 ap（不含自批判断）。
func IsApprover(a Actor, ap *ent.Approval) bool {
	if a.OrgID != ap.OrgID {
		return false
	}
	if len(ap.ApproverIds) == 0 {
		return a.CanManage
	}
	return slices.Contains(ap.ApproverIds, a.ID)
}

// CanView 发起人或审批人可见；其余一律不可见（API 返回 404）。
func CanView(a Actor, ap *ent.Approval) bool {
	return ap.OrgID == a.OrgID && (ap.RequesterID == a.ID || IsApprover(a, ap))
}

// Get 读取审批单（到期的待审批单实时转为 expired）。不可见返回 ErrNotFound。
func (s *Service) Get(ctx context.Context, a Actor, id int) (*ent.Approval, error) {
	ap, err := s.DB.Approval.Query().Where(approval.ID(id), approval.OrgID(a.OrgID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !CanView(a, ap) {
		return nil, ErrNotFound
	}
	return s.expireIfDue(ctx, ap)
}

// expireIfDue 到期的待审批单落库为 expired（条件更新，幂等）。
func (s *Service) expireIfDue(ctx context.Context, ap *ent.Approval) (*ent.Approval, error) {
	if ap.Status != approval.StatusPending || s.now().Before(ap.ExpiresAt) {
		return ap, nil
	}
	n, err := s.DB.Approval.Update().Where(approval.ID(ap.ID), approval.StatusEQ(approval.StatusPending)).
		SetStatus(approval.StatusExpired).SetDecidedAt(s.now()).Save(ctx)
	if err != nil {
		return nil, err
	}
	fresh, err := s.DB.Approval.Get(ctx, ap.ID)
	if err != nil {
		return nil, err
	}
	if n == 1 {
		s.result(ctx, Actor{OrgID: ap.OrgID, Email: "system"}, "approval.expire", fresh, "expired", nil)
	}
	return fresh, nil
}

// ExpireDue 定时任务：把所有到期的待审批单落库为 expired，返回数量。
func (s *Service) ExpireDue(ctx context.Context) (int, error) {
	due, err := s.DB.Approval.Query().Where(approval.StatusEQ(approval.StatusPending), approval.ExpiresAtLTE(s.now())).
		Limit(500).All(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ap := range due {
		fresh, err := s.expireIfDue(ctx, ap)
		if err != nil {
			return n, err
		}
		if fresh.Status == approval.StatusExpired {
			n++
		}
	}
	return n, nil
}

// Decide 审批通过或拒绝。通过后立即用快照参数执行恰好一次（见 RunApproved）。
func (s *Service) Decide(ctx context.Context, a Actor, id int, approve bool, comment string) (*ent.Approval, error) {
	ap, err := s.Get(ctx, a, id)
	if err != nil {
		return nil, err
	}
	// 能看到单子的发起人一律 403（不能自批）；其余非审批人在 Get 中已是 404。
	if ap.RequesterID == a.ID {
		return nil, ErrSelfApproval
	}
	if !IsApprover(a, ap) {
		return nil, ErrNotFound
	}
	if ap.Status != approval.StatusPending {
		return ap, ErrNotPending
	}
	st := approval.StatusRejected
	action := "approval.reject"
	if approve {
		st, action = approval.StatusApproved, "approval.approve"
	}
	if len([]rune(comment)) > 1000 {
		comment = string([]rune(comment)[:1000])
	}
	n, err := s.DB.Approval.Update().Where(approval.ID(id), approval.StatusEQ(approval.StatusPending), approval.ExpiresAtGT(s.now())).
		SetStatus(st).SetDecidedBy(a.ID).SetDecidedAt(s.now()).SetDecisionComment(comment).Save(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		fresh, err := s.Get(ctx, a, id)
		if err != nil {
			return nil, err
		}
		return fresh, ErrNotPending
	}
	ap, err = s.DB.Approval.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.result(ctx, a, action, ap, "success", map[string]any{"comment": comment})
	if approve {
		return s.RunApproved(ctx, ap)
	}
	return ap, nil
}

// Withdraw 发起人撤回待审批单。
func (s *Service) Withdraw(ctx context.Context, a Actor, id int) (*ent.Approval, error) {
	ap, err := s.Get(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if ap.RequesterID != a.ID {
		return nil, ErrNotFound
	}
	n, err := s.DB.Approval.Update().Where(approval.ID(id), approval.StatusEQ(approval.StatusPending)).
		SetStatus(approval.StatusWithdrawn).SetDecidedAt(s.now()).Save(ctx)
	if err != nil {
		return nil, err
	}
	fresh, err := s.DB.Approval.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return fresh, ErrNotPending
	}
	s.result(ctx, a, "approval.withdraw", fresh, "success", nil)
	return fresh, nil
}

// RunApproved 已通过的单子用快照参数执行恰好一次：none → running 的条件更新只有一个调用方能成功。
// 执行时重新按策略判定工具是否可用；审计写失败时回到 none（之后可由「继续」重试），不调用远程。
func (s *Service) RunApproved(ctx context.Context, ap *ent.Approval) (*ent.Approval, error) {
	if ap.Status != approval.StatusApproved {
		return ap, nil
	}
	n, err := s.DB.Approval.Update().Where(approval.ID(ap.ID), approval.StatusEQ(approval.StatusApproved),
		approval.ExecStatusEQ(approval.ExecStatusNone)).SetExecStatus(approval.ExecStatusRunning).Save(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return s.DB.Approval.Get(ctx, ap.ID)
	}
	ctx = audit.WithTrace(ctx, ap.TraceID)
	finish := func(res Result) (*ent.Approval, error) {
		st := approval.ExecStatusSucceeded
		if res.IsError {
			st = approval.ExecStatusFailed
		}
		return s.DB.Approval.UpdateOneID(ap.ID).SetExecStatus(st).SetResult(res.Text).SetResultIsError(res.IsError).
			SetExecutedAt(s.now()).Save(context.WithoutCancel(ctx))
	}
	t, err := s.DB.Tool.Query().Where(tool.ID(ap.ToolID)).Only(ctx)
	if ent.IsNotFound(err) {
		return finish(Result{Text: "工具已被删除，未执行", IsError: true})
	}
	if err != nil {
		_, _ = s.DB.Approval.UpdateOneID(ap.ID).SetExecStatus(approval.ExecStatusNone).Save(context.WithoutCancel(ctx))
		return nil, err
	}
	srv, dec, err := s.Usable(ctx, ap.OrgID, t)
	if err != nil {
		_, _ = s.DB.Approval.UpdateOneID(ap.ID).SetExecStatus(approval.ExecStatusNone).Save(context.WithoutCancel(ctx))
		return nil, err
	}
	if !dec.Usable {
		return finish(Result{Text: "工具当前不可用（" + dec.Reason + "），未执行", IsError: true})
	}
	req, err := s.DB.User.Get(ctx, ap.RequesterID)
	email := ""
	if err == nil {
		email = req.Email
	}
	res, err := s.Execute(ctx, Actor{OrgID: ap.OrgID, ID: ap.RequesterID, Email: email}, t, srv, ap.Args, ap.ID)
	if errors.Is(err, ErrAudit) {
		_, _ = s.DB.Approval.UpdateOneID(ap.ID).SetExecStatus(approval.ExecStatusNone).Save(context.WithoutCancel(ctx))
		fresh, gerr := s.DB.Approval.Get(context.WithoutCancel(ctx), ap.ID)
		if gerr != nil {
			return nil, gerr
		}
		return fresh, ErrAudit
	}
	return finish(res)
}

// ModelResult 审批结束后交回模型的工具结果文本；仍待审批时 ok=false。
func ModelResult(ap *ent.Approval) (Result, bool) {
	switch ap.Status {
	case approval.StatusRejected:
		msg := "审批人拒绝了该工具调用，未执行。"
		if ap.DecisionComment != "" {
			msg += "审批意见：" + ap.DecisionComment
		}
		return Result{Text: msg, IsError: true}, true
	case approval.StatusExpired:
		return Result{Text: "审批超时，按拒绝处理，未执行。", IsError: true}, true
	case approval.StatusWithdrawn:
		return Result{Text: "用户撤回了该工具调用，未执行。", IsError: true}, true
	case approval.StatusApproved:
		if ap.ExecStatus == approval.ExecStatusSucceeded || ap.ExecStatus == approval.ExecStatusFailed {
			return Result{Text: ap.Result, IsError: ap.ResultIsError}, true
		}
	}
	return Result{}, false
}
