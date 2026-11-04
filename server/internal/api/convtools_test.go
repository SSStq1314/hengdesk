package api_test

// M1 第五块 PR4：会话里的工具调用。自动执行、审批挂起与继续、拒绝结果交回模型、步数上限、授权现算。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/ent/tool"
)

type toolEnv struct {
	*mcpEnv
	query, create *ent.Tool
	mem, appr     *client
	conv          int
}

func newToolEnv(t *testing.T, opts ...func(*api.Deps)) *toolEnv {
	t.Helper()
	e := newMCPEnv(t, opts...)
	ctx := context.Background()
	srv := e.register(t, "streamable_http", "/mcp")
	if code, _, body := e.discover(t, srv.ID); code != 200 {
		t.Fatalf("discover %d %s", code, body)
	}
	org := e.c.Organization.Query().FirstX(ctx)
	appr := e.addUser(t, "appr@x.local", org, "", nil)
	memID := atoi(e.uid(t, "member@x.local"))
	grantUse(t, e.gwEnv, e.c.Model.Query().Where(model.Name("ds-flash")).OnlyIDX(ctx), memID)
	q := e.c.Tool.Query().Where(tool.Name("query_ticket")).OnlyX(ctx)
	q = e.c.Tool.UpdateOne(q).SetEnabled(true).SetNeedsReview(false).SetRiskLevel("read_only").SetExecMode("auto").SaveX(ctx)
	cr := e.c.Tool.Query().Where(tool.Name("create_ticket")).OnlyX(ctx)
	cr = e.c.Tool.UpdateOne(cr).SetEnabled(true).SetNeedsReview(false).SetRiskLevel("write").SetExecMode("require_approval").
		SetApproverIds([]int{appr.ID}).SaveX(ctx)
	for _, id := range []int{q.ID, cr.ID} {
		if rec := e.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/tools/%d/acl", id),
			fmt.Sprintf(`{"subject_type":"user","subject_id":%d}`, memID), true); rec.Code != 204 {
			t.Fatalf("grant %d %s", rec.Code, rec.Body)
		}
	}
	te := &toolEnv{mcpEnv: e, query: q, create: cr, mem: e.loginAs(t, "member@x.local", "password123"),
		appr: e.loginAs(t, "appr@x.local", "password123")}
	te.conv = newConv(t, te.mem, `{"model":"ds-flash"}`)
	return te
}

func (te *toolEnv) useTools(t *testing.T, ids ...int) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"tool_ids": ids})
	if rec := te.mem.do(http.MethodPatch, fmt.Sprintf("/api/v1/conversations/%d", te.conv), string(b), true); rec.Code != 200 {
		t.Fatalf("select tools %d %s", rec.Code, rec.Body)
	}
}

func (te *toolEnv) send(t *testing.T, text string) string {
	t.Helper()
	rec := te.mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", te.conv), fmt.Sprintf(`{"content":%q}`, text), true)
	if rec.Code != 200 {
		t.Fatalf("send %d %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func lastAssistant(t *testing.T, te *toolEnv) *ent.Message {
	t.Helper()
	return te.c.Message.Query().Where(message.ConversationID(te.conv), message.RoleEQ(message.RoleAssistant), message.SupersededAtIsNil()).
		Order(ent.Desc(message.FieldID)).FirstX(context.Background())
}

func TestConvToolAuto(t *testing.T) {
	te := newToolEnv(t)
	// 没有授权的工具不能选（与不存在一致 404）
	other := te.c.Tool.Query().Where(tool.Name("create_ticket")).OnlyX(context.Background())
	te.c.Tool.UpdateOne(other).SetEnabled(false).ExecX(context.Background())
	b, _ := json.Marshal(map[string]any{"tool_ids": []int{other.ID}})
	if rec := te.mem.do(http.MethodPatch, fmt.Sprintf("/api/v1/conversations/%d", te.conv), string(b), true); rec.Code != 404 {
		t.Fatalf("禁用工具应 404：%d", rec.Code)
	}
	te.useTools(t, te.query.ID)
	te.m.ToolArgs = `{"ticket_id":"T-1"}`
	body := te.send(t, "查一下 T-1")
	if !strings.Contains(body, "event: tool_call") || !strings.Contains(body, `"status":"succeeded"`) || strings.Count(body, "event: done") != 1 {
		t.Fatalf("SSE %s", body)
	}
	if n := len(te.mock.Calls("query_ticket")); n != 1 {
		t.Fatalf("query calls %d", n)
	}
	m := lastAssistant(t, te)
	if m.Status != message.StatusDone || m.Content != "工单 T-1 状态：处理中。" || len(m.ToolCalls) != 1 || m.ToolCalls[0].Tool != "query_ticket" {
		t.Fatalf("msg %+v", m)
	}
	last := te.m.Requests[len(te.m.Requests)-1]
	msgs, _ := last["messages"].([]any)
	if mm, _ := msgs[len(msgs)-1].(map[string]any); mm["role"] != "tool" {
		t.Fatalf("工具结果应交回模型 %v", msgs)
	}
	ctx := context.Background()
	if !te.c.AuditLog.Query().Where(auditlog.Action("tool.call"), auditlog.PhaseEQ(auditlog.PhaseAttempt)).ExistX(ctx) {
		t.Fatal("tool.call 审计")
	}
	// 选用之后被禁用：不再提供给模型
	te.c.Tool.UpdateOne(te.query).SetEnabled(false).ExecX(ctx)
	te.send(t, "再查一次")
	if _, ok := te.m.Requests[len(te.m.Requests)-1]["tools"]; ok {
		t.Fatal("禁用的工具不应提供给模型")
	}
}

func TestConvToolStepLimit(t *testing.T) {
	te := newToolEnv(t)
	te.useTools(t, te.query.ID)
	te.m.ToolArgs, te.m.AlwaysTool = `{"ticket_id":"T-1"}`, true
	body := te.send(t, "一直查")
	if n := len(te.mock.Calls("query_ticket")); n != 5 {
		t.Fatalf("应在 5 步停止：%d", n)
	}
	if !strings.Contains(body, "5 步上限") || lastAssistant(t, te).Status != message.StatusDone {
		t.Fatalf("body %s", body)
	}
}

func TestConvToolApprovalContinue(t *testing.T) {
	te := newToolEnv(t)
	ctx := context.Background()
	te.useTools(t, te.create.ID)
	te.m.ToolArgs = `{"title":"打印机坏了","token":"s3cr3t"}`
	body := te.send(t, "帮我报修打印机")
	if !strings.Contains(body, "event: approval_pending") || !strings.Contains(body, `"status":"awaiting_approval"`) {
		t.Fatalf("SSE %s", body)
	}
	if n := len(te.mock.Calls("create_ticket")); n != 0 {
		t.Fatalf("审批前不得执行 %d", n)
	}
	m := lastAssistant(t, te)
	if m.Status != message.StatusAwaitingApproval || len(m.ToolCalls) != 1 || m.ToolCalls[0].ApprovalID == 0 ||
		strings.Contains(m.ToolCalls[0].ArgsSummary, "s3cr3t") {
		t.Fatalf("msg %+v", m.ToolCalls)
	}
	apID := m.ToolCalls[0].ApprovalID
	cont := fmt.Sprintf("/api/v1/conversations/%d/messages/%d/continue", te.conv, m.ID)
	if rec := te.mem.do(http.MethodPost, cont, ``, true); rec.Code != 409 {
		t.Fatalf("审批中继续应 409：%d", rec.Code)
	}
	if rec := te.mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", te.conv), `{"content":"hi"}`, true); rec.Code != 409 {
		t.Fatalf("等待审批时不能发新消息：%d", rec.Code)
	}
	if rec := te.appr.do(http.MethodPost, cont, ``, true); rec.Code != 404 {
		t.Fatalf("他人继续应 404：%d", rec.Code)
	}
	if rec := te.appr.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/approve", apID), `{}`, true); rec.Code != 200 {
		t.Fatalf("approve %d %s", rec.Code, rec.Body)
	}
	if n := len(te.mock.Calls("create_ticket")); n != 1 {
		t.Fatalf("通过后执行一次 %d", n)
	}
	rec := te.mem.do(http.MethodPost, cont, ``, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "event: done") {
		t.Fatalf("continue %d %s", rec.Code, rec.Body)
	}
	m = lastAssistant(t, te)
	if m.Status != message.StatusDone || m.Content != "工单 T-1 状态：处理中。" || m.ToolCalls[0].Status != "succeeded" {
		t.Fatalf("after continue %+v", m)
	}
	if n := len(te.mock.Calls("create_ticket")); n != 1 {
		t.Fatalf("继续不得重复执行 %d", n)
	}
	if rec := te.mem.do(http.MethodPost, cont, ``, true); rec.Code != 409 {
		t.Fatalf("重复继续 %d", rec.Code)
	}
	// 同一条 trace：审批单、工具调用与会话审计串在一起
	ap := te.c.Approval.GetX(ctx, apID)
	if ap.TraceID == "" || !te.c.AuditLog.Query().Where(auditlog.Action("tool.call"), auditlog.TraceID(ap.TraceID)).ExistX(ctx) {
		t.Fatalf("trace %q", ap.TraceID)
	}

	// 拒绝：结果交回模型，不执行
	te.send(t, "再报修一次")
	m = lastAssistant(t, te)
	apID = m.ToolCalls[0].ApprovalID
	if rec := te.appr.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/reject", apID), `{"comment":"重复报修"}`, true); rec.Code != 200 {
		t.Fatalf("reject %d", rec.Code)
	}
	cont = fmt.Sprintf("/api/v1/conversations/%d/messages/%d/continue", te.conv, m.ID)
	if rec := te.mem.do(http.MethodPost, cont, ``, true); rec.Code != 200 {
		t.Fatalf("continue %d %s", rec.Code, rec.Body)
	}
	if n := len(te.mock.Calls("create_ticket")); n != 1 {
		t.Fatalf("拒绝不得执行 %d", n)
	}
	last, _ := json.Marshal(te.m.Requests[len(te.m.Requests)-1]["messages"])
	if !strings.Contains(string(last), "重复报修") {
		t.Fatalf("拒绝结果应交回模型 %s", last)
	}
	if m = lastAssistant(t, te); m.Status != message.StatusDone || m.ToolCalls[0].Status != "rejected" {
		t.Fatalf("rejected %+v", m.ToolCalls)
	}
}

type failToolAudit struct{ audit.Writer }

func (w failToolAudit) Write(ctx context.Context, e audit.Entry) error {
	if e.Action == "tool.call" && e.Phase == "attempt" {
		return errors.New("audit down")
	}
	return w.Writer.Write(ctx, e)
}

// tool.call 审计写失败：不执行工具，回复以 audit_failed 结束。
func TestConvToolAuditFailClosed(t *testing.T) {
	te := newToolEnv(t, func(d *api.Deps) { d.Audit = failToolAudit{d.Audit} })
	te.useTools(t, te.query.ID)
	te.m.ToolArgs = `{"ticket_id":"T-1"}`
	body := te.send(t, "查一下")
	if n := len(te.mock.Calls("query_ticket")); n != 0 {
		t.Fatalf("审计失败不得执行 %d", n)
	}
	if m := lastAssistant(t, te); m.Status != message.StatusError || m.ErrorCode != "audit_failed" || !strings.Contains(body, "event: error") {
		t.Fatalf("msg %+v %s", m, body)
	}
}
