package api_test

// D-44：审批默认做法。高危不能自动（含超级管理员）、执行前复核发起人权限、审批人为空时提醒管理员、
// 审批有结果后 worker 自动续写（幂等、不并发）、撤回后自动续写、重启恢复（AC-08：任务可重投，工具不重复执行）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/approval"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	entapproval "github.com/SSStq1314/hengdesk/server/internal/ent/approval"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/ent/notification"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

type contEnv struct {
	*toolEnv
	deps   *api.Deps
	mu     sync.Mutex
	queued []int
	drop   bool
}

func newContEnv(t *testing.T) *contEnv {
	t.Helper()
	ce := &contEnv{}
	ce.toolEnv = newToolEnv(t, func(d *api.Deps) {
		ce.deps = d
		d.ContinueEnqueue = func(_ context.Context, id int) error {
			ce.mu.Lock()
			defer ce.mu.Unlock()
			if ce.drop {
				return errors.New("queue down")
			}
			ce.queued = append(ce.queued, id)
			return nil
		}
	})
	return ce
}

// worker 新建一个续写器（每次都是全新实例，等同于 worker 重启后处理任务）。
func (ce *contEnv) worker() *api.Continuer { return api.NewContinuer(*ce.deps) }

func (ce *contEnv) take() []int {
	ce.mu.Lock()
	defer ce.mu.Unlock()
	q := ce.queued
	ce.queued = nil
	return q
}

// drain 像 worker 一样处理已投递的任务（同一任务投两次，模拟重投）。
func (ce *contEnv) drain(t *testing.T) {
	t.Helper()
	ids := ce.take()
	if len(ids) == 0 {
		t.Fatal("没有投递续写任务")
	}
	for _, id := range ids {
		for range 2 {
			if err := ce.worker().Continue(context.Background(), id); err != nil {
				t.Fatalf("continue %d: %v", id, err)
			}
		}
	}
}

func (ce *contEnv) awaiting(t *testing.T) (*ent.Message, int) {
	t.Helper()
	ce.useTools(t, ce.create.ID)
	ce.m.ToolArgs = `{"title":"打印机坏了"}`
	ce.send(t, "帮我报修打印机")
	m := lastAssistant(t, ce.toolEnv)
	if m.Status != message.StatusAwaitingApproval || len(m.ToolCalls) != 1 || m.ToolCalls[0].ApprovalID == 0 {
		t.Fatalf("应挂起等待审批 %+v", m)
	}
	return m, m.ToolCalls[0].ApprovalID
}

func (ce *contEnv) notes(t *testing.T, email string) []*ent.Notification {
	t.Helper()
	uid := ce.c.User.Query().Where(user.Email(email)).OnlyIDX(context.Background())
	return ce.c.Notification.Query().Where(notification.UserID(uid), notification.Kind(approval.NotifyKind)).AllX(context.Background())
}

// 第 10 条：高危工具任何人（含超级管理员）都不能设为自动执行；库里被写成 auto 也按审批处理。
func TestD44HighRiskNeverAutoEvenSuperAdmin(t *testing.T) {
	ce := newContEnv(t)
	ctx := context.Background()
	admin := ce.c.User.Query().Where(user.Email("admin@x.local")).WithMemberships(func(q *ent.MembershipQuery) { q.WithRole() }).OnlyX(ctx)
	isSuper := false
	for _, m := range admin.Edges.Memberships {
		isSuper = isSuper || m.Edges.Role.Key == rbac.SuperAdmin
	}
	if !isSuper {
		t.Fatal("前提：admin 是超级管理员")
	}
	if code, _, _ := ce.patchTool(ce.create.ID, `{"risk_level":"high_risk","exec_mode":"auto"}`); code != 400 {
		t.Fatalf("超级管理员 high+auto 应 400：%d", code)
	}
	if code, _, _ := ce.patchTool(ce.create.ID, `{"risk_level":"high_risk"}`); code != 200 {
		t.Fatal("set high")
	}
	if code, _, _ := ce.patchTool(ce.create.ID, `{"exec_mode":"auto"}`); code != 400 {
		t.Fatalf("高危改 auto 应 400：%d", code)
	}
	// 兜底：库里被直接写成 high_risk + auto，超级管理员自己调用也要审批。
	ce.c.Tool.UpdateOneID(ce.create.ID).SetRiskLevel("high_risk").SetExecMode("auto").ExecX(ctx)
	if rec := ce.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/tools/%d/acl", ce.create.ID),
		fmt.Sprintf(`{"subject_type":"user","subject_id":%d}`, admin.ID), true); rec.Code != 204 {
		t.Fatalf("grant %d", rec.Code)
	}
	grantUse(t, ce.gwEnv, ce.c.Model.Query().Where(model.Name("ds-flash")).OnlyIDX(ctx), admin.ID)
	conv := newConv(t, ce.admin, fmt.Sprintf(`{"model":"ds-flash","tool_ids":[%d]}`, ce.create.ID))
	ce.m.ToolArgs = `{"title":"x"}`
	if rec := ce.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", conv), `{"content":"建单"}`, true); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), "event: approval_pending") {
		t.Fatalf("超级管理员调用高危工具也要审批 %d %s", rec.Code, rec.Body)
	}
	if n := len(ce.mock.Calls("create_ticket")); n != 0 {
		t.Fatalf("不得自动执行 %d", n)
	}
}

// 第 11 条：审批通过后、执行前发起人已被收回授权：不执行，原因作为工具结果交回模型，并写审计。
func TestD44RecheckACLBeforeExec(t *testing.T) {
	ce := newContEnv(t)
	ctx := context.Background()
	m, apID := ce.awaiting(t)
	memID := ce.uid(t, "member@x.local")
	if rec := ce.admin.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/tools/%d/acl/user/%s", ce.create.ID, memID), ``, true); rec.Code != 204 {
		t.Fatalf("revoke %d %s", rec.Code, rec.Body)
	}
	if rec := ce.appr.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/approve", apID), `{}`, true); rec.Code != 200 {
		t.Fatalf("approve %d %s", rec.Code, rec.Body)
	}
	if n := len(ce.mock.Calls("create_ticket")); n != 0 {
		t.Fatalf("无权限不得执行 %d", n)
	}
	ap := ce.c.Approval.GetX(ctx, apID)
	if ap.ExecStatus != entapproval.ExecStatusFailed || !strings.Contains(ap.Result, "使用权限") {
		t.Fatalf("ap %+v", ap)
	}
	if !ce.c.AuditLog.Query().Where(auditlog.Action("tool.call"), auditlog.Outcome("denied")).ExistX(ctx) {
		t.Fatal("应写 denied 审计")
	}
	ce.drain(t)
	m = ce.c.Message.GetX(ctx, m.ID)
	if m.Status != message.StatusDone || m.ToolCalls[0].Status != "failed" {
		t.Fatalf("msg %+v", m)
	}
	last, _ := json.Marshal(ce.m.LastRequest()["messages"])
	if !strings.Contains(string(last), "使用权限") {
		t.Fatalf("原因应交回模型 %s", last)
	}
}

// 第 13 条：审批人为空时提醒本组织所有拥有 tool.manage 的管理员（不提醒普通成员）。
func TestD44NotifyManagersWhenNoApprovers(t *testing.T) {
	ce := newContEnv(t)
	ctx := context.Background()
	ce.c.Tool.UpdateOneID(ce.create.ID).ClearApproverIds().ExecX(ctx)
	_, apID := ce.awaiting(t)
	ns := ce.notes(t, "admin@x.local")
	if len(ns) != 1 || !strings.Contains(ns[0].Title, "待审批") {
		t.Fatalf("管理员应收到提醒 %+v", ns)
	}
	if len(ce.notes(t, "appr@x.local")) != 0 || len(ce.notes(t, "member@x.local")) != 0 {
		t.Fatal("没有 tool.manage 的人不应收到")
	}
	if rec := ce.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/approve", apID), `{}`, true); rec.Code != 200 {
		t.Fatalf("管理员可以审批 %d", rec.Code)
	}
}

// 第 15 条：审批通过后 worker 自动续写并提醒发起人；任务重投 / 重复处理不重复执行工具。
func TestD44AutoContinueOnApprove(t *testing.T) {
	ce := newContEnv(t)
	ctx := context.Background()
	m, apID := ce.awaiting(t)
	if rec := ce.appr.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/approve", apID), `{}`, true); rec.Code != 200 {
		t.Fatalf("approve %d", rec.Code)
	}
	ce.drain(t)
	m = ce.c.Message.GetX(ctx, m.ID)
	if m.Status != message.StatusDone || m.Content != "工单 T-1 状态：处理中。" || m.ErrorCode != "" {
		t.Fatalf("应自动续写完成 %+v", m)
	}
	if n := len(ce.mock.Calls("create_ticket")); n != 1 {
		t.Fatalf("恰好执行一次 %d", n)
	}
	ns := ce.notes(t, "member@x.local")
	if len(ns) != 1 || !strings.Contains(ns[0].Title, "已通过") {
		t.Fatalf("发起人提醒 %+v", ns)
	}
	if !ce.c.AuditLog.Query().Where(auditlog.Action("chat.message.continue"), auditlog.ActorEmail("member@x.local")).ExistX(ctx) {
		t.Fatal("续写审计以发起人身份记录")
	}
	// 前端看到的会话里是最终结果（轮询即可）
	rec := ce.mem.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", ce.conv), ``, false)
	if !strings.Contains(rec.Body.String(), `"status":"done"`) {
		t.Fatalf("get %s", rec.Body)
	}
}

// 第 15 条：拒绝 / 超时同样自动续写，结果交回模型。
func TestD44AutoContinueOnRejectAndExpire(t *testing.T) {
	ce := newContEnv(t)
	ctx := context.Background()
	m, apID := ce.awaiting(t)
	if rec := ce.appr.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/reject", apID), `{"comment":"不需要"}`, true); rec.Code != 200 {
		t.Fatalf("reject %d", rec.Code)
	}
	ce.drain(t)
	if m = ce.c.Message.GetX(ctx, m.ID); m.Status != message.StatusDone || m.ToolCalls[0].Status != "rejected" {
		t.Fatalf("reject %+v", m)
	}
	m, apID = ce.awaiting(t)
	ce.c.Approval.UpdateOneID(apID).SetExpiresAt(time.Now().Add(-time.Minute)).ExecX(ctx)
	if n, err := ce.worker().Approvals().ExpireDue(ctx); err != nil || n != 1 {
		t.Fatalf("expire %d %v", n, err)
	}
	ce.drain(t)
	if m = ce.c.Message.GetX(ctx, m.ID); m.Status != message.StatusDone {
		t.Fatalf("expire %+v", m)
	}
	last, _ := json.Marshal(ce.m.LastRequest()["messages"])
	if !strings.Contains(string(last), "审批超时") {
		t.Fatalf("%s", last)
	}
	if n := len(ce.mock.Calls("create_ticket")); n != 0 {
		t.Fatalf("不得执行 %d", n)
	}
}

// 第 16 条：等待审批期间会话锁定；撤回后解锁并自动续写，告诉模型「已撤回」。
func TestD44WithdrawUnlocksAndContinues(t *testing.T) {
	ce := newContEnv(t)
	ctx := context.Background()
	m, apID := ce.awaiting(t)
	if rec := ce.mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", ce.conv), `{"content":"hi"}`, true); rec.Code != 409 {
		t.Fatalf("等待审批时发消息应 409：%d", rec.Code)
	}
	if rec := ce.mem.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/withdraw", apID), `{}`, true); rec.Code != 200 {
		t.Fatalf("withdraw %d %s", rec.Code, rec.Body)
	}
	ce.drain(t)
	m = ce.c.Message.GetX(ctx, m.ID)
	if m.Status != message.StatusDone || m.ToolCalls[0].Status != "rejected" {
		t.Fatalf("msg %+v", m)
	}
	last, _ := json.Marshal(ce.m.LastRequest()["messages"])
	if !strings.Contains(string(last), "撤回") {
		t.Fatalf("应告诉模型已撤回 %s", last)
	}
	if n := len(ce.mock.Calls("create_ticket")); n != 0 {
		t.Fatalf("撤回不得执行 %d", n)
	}
	// 已解锁：可以继续发消息
	ce.m.ToolArgs = ""
	ce.useTools(t)
	ce.send(t, "好的")
}

// 同一会话不能并发续写：锁被占用时返回 ErrContinueBusy（asynq 重试），消息保持等待。
// 自动续写失败（无可用模型）时标记 auto_continue_failed，前端才显示「继续」；手动继续成功后清除标记。
func TestD44ContinueBusyAndFailure(t *testing.T) {
	ce := newContEnv(t)
	ctx := context.Background()
	m, apID := ce.awaiting(t)
	if rec := ce.appr.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/approve", apID), `{}`, true); rec.Code != 200 {
		t.Fatalf("approve %d", rec.Code)
	}
	ids := ce.take()
	key := fmt.Sprintf("hengdesk:chat:gen:%d", ce.conv)
	_ = ce.mr.Set(key, "other")
	if err := ce.worker().Continue(ctx, ids[0]); !errors.Is(err, api.ErrContinueBusy) {
		t.Fatalf("锁被占用应重试：%v", err)
	}
	if m = ce.c.Message.GetX(ctx, m.ID); m.Status != message.StatusAwaitingApproval || m.ErrorCode != "" {
		t.Fatalf("msg %+v", m)
	}
	ce.mr.Del(key)
	ce.c.Model.Update().SetEnabled(false).ExecX(ctx)
	if err := ce.worker().Continue(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if m = ce.c.Message.GetX(ctx, m.ID); m.Status != message.StatusAwaitingApproval || m.ErrorCode != api.AutoContinueFailed {
		t.Fatalf("应标记自动续写失败 %+v", m)
	}
	ns := ce.notes(t, "member@x.local")
	if len(ns) != 1 || !strings.Contains(ns[0].Title, "未能自动继续") {
		t.Fatalf("失败提醒 %+v", ns)
	}
	// 失败标记的消息不会被定时补投反复重试
	if n, _ := ce.worker().Sweep(ctx, func(context.Context, int) error { return nil }); n != 0 {
		t.Fatalf("sweep %d", n)
	}
	ce.c.Model.Update().SetEnabled(true).ExecX(ctx)
	rec := ce.mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages/%d/continue", ce.conv, m.ID), ``, true)
	if rec.Code != 200 {
		t.Fatalf("手动继续 %d %s", rec.Code, rec.Body)
	}
	if m = ce.c.Message.GetX(ctx, m.ID); m.Status != message.StatusDone || m.ErrorCode != "" {
		t.Fatalf("msg %+v", m)
	}
	if n := len(ce.mock.Calls("create_ticket")); n != 1 {
		t.Fatalf("执行一次 %d", n)
	}
}

// AC-08：等待审批时重启（投递丢失 / worker 重启）：定时补投找回并续写，工具只执行一次。
func TestD44RestartWhileAwaiting(t *testing.T) {
	ce := newContEnv(t)
	ctx := context.Background()
	m, apID := ce.awaiting(t)
	ce.drop = true // 审批时队列不可用，任务丢失
	if rec := ce.appr.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/approve", apID), `{}`, true); rec.Code != 200 {
		t.Fatalf("approve %d", rec.Code)
	}
	ce.drop = false
	if len(ce.take()) != 0 {
		t.Fatal("投递应失败")
	}
	enq := func(_ context.Context, id int) error { ce.queued = append(ce.queued, id); return nil }
	if n, err := ce.worker().Sweep(ctx, enq); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	ce.drain(t)
	if m = ce.c.Message.GetX(ctx, m.ID); m.Status != message.StatusDone {
		t.Fatalf("msg %+v", m)
	}
	if n := len(ce.mock.Calls("create_ticket")); n != 1 {
		t.Fatalf("执行一次 %d", n)
	}
	if n, _ := ce.worker().Sweep(ctx, enq); n != 0 {
		t.Fatalf("完成后不再补投 %d", n)
	}

	// 审批通过后执行到一半进程崩溃（exec 停在 running）：超时后按结果未知落库，不重试，续写继续。
	m, apID = ce.awaiting(t)
	ce.drop = true
	ce.c.Approval.UpdateOneID(apID).SetStatus(entapproval.StatusApproved).SetExecStatus(entapproval.ExecStatusRunning).
		SetExecutedAt(time.Now().Add(-time.Hour)).ExecX(ctx)
	ce.drop = false
	if n, err := ce.worker().Sweep(ctx, enq); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	ce.drain(t)
	if m = ce.c.Message.GetX(ctx, m.ID); m.Status != message.StatusDone || m.ToolCalls[0].Status != "failed" {
		t.Fatalf("msg %+v", m)
	}
	if n := len(ce.mock.Calls("create_ticket")); n != 1 {
		t.Fatalf("中断的执行不得重试 %d", n)
	}
	if ap := ce.c.Approval.GetX(ctx, apID); ap.Result != approval.StuckExecMsg {
		t.Fatalf("ap %+v", ap)
	}
}

// AC-08：续写过程中重启（消息停在 streaming、锁已过期）：补投后恢复续写；已执行的工具不重复执行，
// 执行到一半的自动工具按「结果未知」交回模型。
func TestD44RestartDuringContinuation(t *testing.T) {
	ce := newContEnv(t)
	ctx := context.Background()
	m, apID := ce.awaiting(t)
	if rec := ce.appr.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/approve", apID), `{}`, true); rec.Code != 200 {
		t.Fatalf("approve %d", rec.Code)
	}
	ce.take()
	// 续写刚把消息置为 streaming 就崩溃了（锁随进程消失）
	ce.c.Message.UpdateOneID(m.ID).SetStatus(message.StatusStreaming).ExecX(ctx)
	enq := func(_ context.Context, id int) error { ce.queued = append(ce.queued, id); return nil }
	_ = ce.mr.Set(fmt.Sprintf("hengdesk:chat:gen:%d", ce.conv), "live")
	if n, _ := ce.worker().Sweep(ctx, enq); n != 0 {
		t.Fatal("锁还在（仍在生成）时不应补投")
	}
	ce.mr.Del(fmt.Sprintf("hengdesk:chat:gen:%d", ce.conv))
	if n, err := ce.worker().Sweep(ctx, enq); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	ce.drain(t)
	if m = ce.c.Message.GetX(ctx, m.ID); m.Status != message.StatusDone || m.Content != "工单 T-1 状态：处理中。" {
		t.Fatalf("msg %+v", m)
	}
	if n := len(ce.mock.Calls("create_ticket")); n != 1 {
		t.Fatalf("不得重复执行 %d", n)
	}

	// 自动工具执行到一半崩溃：检查点里是 running → 恢复时不再执行
	ce.useTools(t, ce.query.ID)
	ce.m.ToolArgs = `{"ticket_id":"T-1"}`
	ce.send(t, "查一下")
	before := len(ce.mock.Calls("query_ticket"))
	m = lastAssistant(t, ce.toolEnv)
	call := `{"id":"c9","type":"function","function":{"name":"query_ticket","arguments":"{}"}}`
	st := `{"trace":[{"role":"assistant","content":"","reasoning_content":"先查","tool_calls":[` + call + `]}],"queue":[` + call + `],"steps":1,` +
		`"calls":[{"id":"c9","tool":"query_ticket","status":"running"}]}`
	ce.c.Message.UpdateOneID(m.ID).SetStatus(message.StatusStreaming).SetToolState([]byte(st)).ExecX(ctx)
	if n, err := ce.worker().Sweep(ctx, enq); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	ce.drain(t)
	if n := len(ce.mock.Calls("query_ticket")); n != before {
		t.Fatalf("中断的自动工具不得重复执行 %d→%d", before, n)
	}
	if m = ce.c.Message.GetX(ctx, m.ID); m.Status != message.StatusDone {
		t.Fatalf("msg %+v", m)
	}
	last, _ := json.Marshal(ce.m.LastRequest()["messages"])
	if !strings.Contains(string(last), "结果未知") {
		t.Fatalf("%s", last)
	}
}

// 审批通过后工具已执行，续写模型被预算拦截：工具结果必须保留在会话里，失败原因是可读的预算提示而不是「Too Many Requests」。
// 顺序保持「先执行已批准的工具，再续写模型」：批准意味着工具应当执行；预算只约束后续模型调用。结果不丢，由用户在会话中看到并提示预算用尽。
func TestD44ContinueBudgetExceededKeepsToolResult(t *testing.T) {
	ce := newContEnv(t)
	ctx := context.Background()
	m, apID := ce.awaiting(t)
	if rec := ce.appr.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/approve", apID), `{}`, true); rec.Code != 200 {
		t.Fatalf("approve %d %s", rec.Code, rec.Body)
	}
	if n := len(ce.mock.Calls("create_ticket")); n != 1 {
		t.Fatalf("审批通过后应已执行工具 %d", n)
	}
	// 把预算压到已超额后再续写（第一次发送已消耗 token）。工具结果在续写入口同步进会话，即使模型被拦截也可见。
	if rec := ce.admin.do(http.MethodPost, "/api/v1/admin/budgets", `{"scope":"org","period":"day","metric":"tokens","limit":1}`, true); rec.Code != 201 {
		t.Fatalf("budget %d %s", rec.Code, rec.Body)
	}
	ids := ce.take()
	if len(ids) == 0 {
		t.Fatal("没有投递续写任务")
	}
	if err := ce.worker().Continue(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	m = ce.c.Message.GetX(ctx, m.ID)
	if m.Status != message.StatusAwaitingApproval || m.ErrorCode != api.AutoContinueFailed {
		t.Fatalf("应标记自动续写失败 %+v", m)
	}
	if !strings.Contains(m.Error, "预算已用尽") || strings.Contains(m.Error, "Too Many Requests") {
		t.Fatalf("失败原因应为可读预算提示，得到 %q", m.Error)
	}
	if n := len(ce.mock.Calls("create_ticket")); n != 1 {
		t.Fatalf("工具不得重复执行或丢失 %d", n)
	}
	if len(m.ToolCalls) == 0 || m.ToolCalls[0].Status != "succeeded" || m.ToolCalls[0].ResultSummary == "" {
		t.Fatalf("工具结果必须保留在会话里 %+v", m.ToolCalls)
	}
	ns := ce.notes(t, "member@x.local")
	if len(ns) != 1 || !strings.Contains(ns[0].Title, "预算已用尽") {
		t.Fatalf("发起人提醒应含预算提示 %+v", ns)
	}
	rec := ce.mem.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", ce.conv), ``, false)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"status":"succeeded"`) ||
		!strings.Contains(body, "T-1001") || !strings.Contains(body, "预算已用尽") {
		t.Fatalf("会话应对用户可见工具结果与预算提示 %s", rec.Body)
	}
}
