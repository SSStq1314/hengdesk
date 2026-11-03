package api_test

// M1 第三块 PR3：保留期清理与法律保留（D-40 第 2 条）、trace_id 回放（FR-28）、按 token 截断（D-40 第 4 条）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

func TestChatRetentionAdminAndLegalHold(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	org := g.c.Organization.Query().FirstX(ctx)
	g.addUser(t, "orgadmin@x.local", org, rbac.OrgAdmin, nil)
	g.addUser(t, "auditor@x.local", org, rbac.SecurityAuditor, nil)
	for _, who := range []string{"member@x.local", "orgadmin@x.local", "auditor@x.local"} {
		cl := g.loginAs(t, who, "password123")
		for _, rq := range [][3]string{{"GET", "/api/v1/admin/chat-retention", ""}, {"PUT", "/api/v1/admin/chat-retention", `{"enabled":true,"days":365}`},
			{"POST", "/api/v1/admin/chat-retention/run", ""}, {"GET", "/api/v1/admin/legal-holds", ""}, {"POST", "/api/v1/admin/legal-holds", `{"reason":"x"}`}} {
			if got := cl.do(rq[0], rq[1], rq[2], true).Code; got != 404 {
				t.Errorf("%s %s %s = %d，越权应 404", who, rq[0], rq[1], got)
			}
		}
	}
	a := g.admin
	rec := a.do(http.MethodGet, "/api/v1/admin/chat-retention", "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":false,"days":365`) {
		t.Fatalf("默认设置 %d %s", rec.Code, rec.Body)
	}
	if rec := a.do(http.MethodPost, "/api/v1/admin/chat-retention/run", "", true); rec.Code != 409 {
		t.Fatalf("未开启时手动清理应 409：%d", rec.Code)
	}
	if rec := a.do(http.MethodPut, "/api/v1/admin/chat-retention", `{"enabled":true,"days":90}`, true); rec.Code != 422 {
		t.Fatalf("低于 180 天应 422：%d", rec.Code)
	}
	if rec := a.do(http.MethodPut, "/api/v1/admin/chat-retention", `{"enabled":true,"days":200}`, true); rec.Code != 200 {
		t.Fatalf("put %d %s", rec.Code, rec.Body)
	}
	if g.c.AuditLog.Query().Where(auditlog.Action("chat.retention.update")).CountX(ctx) != 1 {
		t.Fatal("缺少设置审计")
	}
	rec = a.do(http.MethodPost, "/api/v1/admin/chat-retention/run", "", true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"conversations":0`) {
		t.Fatalf("run %d %s", rec.Code, rec.Body)
	}
	// 没有可清理的会话时不产生批次审计；有数据的清理、审计字段与法律保留跳过见 chatretention 包测试。

	// 法律保留：必填原因；他组织用户 404；解除后不能重复解除
	if rec := a.do(http.MethodPost, "/api/v1/admin/legal-holds", `{"reason":"  "}`, true); rec.Code != 422 {
		t.Fatalf("空原因应 422：%d", rec.Code)
	}
	o2 := g.c.Organization.Create().SetName("o2").SetSlug("o2").SaveX(ctx)
	stranger := g.addUser(t, "s@o2.local", o2, rbac.Member, nil)
	if rec := a.do(http.MethodPost, "/api/v1/admin/legal-holds", fmt.Sprintf(`{"user_id":%d,"reason":"诉讼"}`, stranger.ID), true); rec.Code != 404 {
		t.Fatalf("他组织用户应 404：%d", rec.Code)
	}
	mid := atoi(g.uid(t, "member@x.local"))
	rec = a.do(http.MethodPost, "/api/v1/admin/legal-holds", fmt.Sprintf(`{"user_id":%d,"reason":"诉讼保全"}`, mid), true)
	if rec.Code != 201 {
		t.Fatalf("create hold %d %s", rec.Code, rec.Body)
	}
	var hv struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &hv)
	if rec := a.do(http.MethodPost, "/api/v1/admin/legal-holds", `{"user_email":"s@o2.local","reason":"x"}`, true); rec.Code != 404 {
		t.Fatalf("他组织邮箱应 404：%d", rec.Code)
	}
	if rec := a.do(http.MethodPost, "/api/v1/admin/legal-holds", `{"user_email":"MEMBER@x.local","reason":"按邮箱"}`, true); rec.Code != 201 ||
		!strings.Contains(rec.Body.String(), fmt.Sprintf(`"user_id":%d`, mid)) {
		t.Fatalf("按邮箱 %d %s", rec.Code, rec.Body)
	}
	if rec := a.do(http.MethodPost, "/api/v1/admin/legal-holds", `{"reason":"全组织"}`, true); rec.Code != 201 {
		t.Fatalf("org hold %d", rec.Code)
	}
	rec = a.do(http.MethodGet, "/api/v1/admin/legal-holds", "", false)
	if !strings.Contains(rec.Body.String(), `"scope":"user"`) || !strings.Contains(rec.Body.String(), `"user_email":"member@x.local"`) ||
		!strings.Contains(rec.Body.String(), `"scope":"org"`) {
		t.Fatalf("list %s", rec.Body)
	}
	foreign := g.c.LegalHold.Create().SetOrgID(o2.ID).SetReason("x").SetCreatedBy(1).SaveX(ctx)
	if rec := a.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/legal-holds/%d", foreign.ID), "", true); rec.Code != 404 {
		t.Fatalf("他组织保留应 404：%d", rec.Code)
	}
	if rec := a.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/legal-holds/%d", hv.ID), "", true); rec.Code != 204 {
		t.Fatalf("release %d", rec.Code)
	}
	if rec := a.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/legal-holds/%d", hv.ID), "", true); rec.Code != 409 {
		t.Fatalf("重复解除应 409：%d", rec.Code)
	}
	for _, act := range []string{"chat.legal_hold.create", "chat.legal_hold.release"} {
		if g.c.AuditLog.Query().Where(auditlog.Action(act)).CountX(ctx) == 0 {
			t.Fatalf("缺少 %s 审计", act)
		}
	}
}

type failRetentionAudit struct{ audit.Writer }

func (w failRetentionAudit) Write(ctx context.Context, e audit.Entry) error {
	if strings.HasPrefix(e.Action, "chat.retention.") || strings.HasPrefix(e.Action, "chat.legal_hold.") {
		return fmt.Errorf("audit down")
	}
	return w.Writer.Write(ctx, e)
}

func TestChatRetentionAuditFailClosed(t *testing.T) {
	g := newGW(t, false, func(d *api.Deps) { d.Audit = failRetentionAudit{d.Audit} })
	ctx := context.Background()
	if rec := g.admin.do(http.MethodPut, "/api/v1/admin/chat-retention", `{"enabled":true,"days":365}`, true); rec.Code != 503 {
		t.Fatalf("审计失败应 503：%d", rec.Code)
	}
	if rec := g.admin.do(http.MethodPost, "/api/v1/admin/legal-holds", `{"reason":"x"}`, true); rec.Code != 503 || g.c.LegalHold.Query().CountX(ctx) != 0 {
		t.Fatalf("审计失败不应创建保留：%d", rec.Code)
	}
}

func TestAuditTraceReplay(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	org := g.c.Organization.Query().FirstX(ctx)
	g.addUser(t, "auditor@x.local", org, rbac.SecurityAuditor, nil)
	id := newConv(t, g.admin, `{}`)
	rec := g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), `{"content":"你好","model":"ds-flash"}`, true)
	tr := rec.Header().Get(audit.HeaderTraceID)
	if rec.Code != 200 || !audit.ValidTraceID(tr) {
		t.Fatalf("send %d trace=%q", rec.Code, tr)
	}
	rows := g.c.AuditLog.Query().Where(auditlog.TraceID(tr)).AllX(ctx)
	acts := map[string]bool{}
	for _, r := range rows {
		acts[r.Action] = true
	}
	for _, want := range []string{"http.POST", "chat.message.send"} {
		if !acts[want] {
			t.Fatalf("同一 trace 应含 %s：%v", want, acts)
		}
	}
	if !acts["model.call"] {
		t.Fatalf("模型调用审计也应带 trace：%v", acts)
	}
	// 每个请求的 trace 不同
	rec2 := g.admin.do(http.MethodGet, "/api/v1/conversations", "", false)
	if t2 := rec2.Header().Get(audit.HeaderTraceID); t2 == "" || t2 == tr {
		t.Fatalf("每个请求应有新的 trace：%q", t2)
	}

	aud := g.loginAs(t, "auditor@x.local", "password123")
	rec = aud.do(http.MethodGet, "/api/v1/admin/audit-logs/traces/"+tr, "", false)
	var tv struct {
		TraceID string
		Count   int
		Items   []struct {
			Action   string
			OffsetMs int64 `json:"offset_ms"`
		}
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &tv) != nil || tv.Count != len(rows) || tv.Items[0].Action != "http.POST" {
		t.Fatalf("trace %d %s", rec.Code, rec.Body)
	}
	for i := 1; i < len(tv.Items); i++ {
		if tv.Items[i].OffsetMs < tv.Items[i-1].OffsetMs {
			t.Fatal("时间线应按时间排序")
		}
	}
	if g.c.AuditLog.Query().Where(auditlog.Action("audit.trace.view")).CountX(ctx) != 1 {
		t.Fatal("查看链路应记审计")
	}
	rec = aud.do(http.MethodGet, "/api/v1/admin/audit-logs?trace_id="+tr, "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"action":"chat.message.send"`) {
		t.Fatalf("按 trace 过滤 %d", rec.Code)
	}
	if got := aud.do(http.MethodGet, "/api/v1/admin/audit-logs?trace_id=xyz", "", false).Code; got != 400 {
		t.Fatalf("非法 trace 过滤应 400：%d", got)
	}
	if got := aud.do(http.MethodGet, "/api/v1/admin/audit-logs/traces/NOT-HEX", "", false).Code; got != 400 {
		t.Fatalf("非法 trace 应 400：%d", got)
	}
	if got := aud.do(http.MethodGet, "/api/v1/admin/audit-logs/traces/"+strings.Repeat("0", 32), "", false).Code; got != 404 {
		t.Fatalf("不存在的 trace 应 404：%d", got)
	}
	// 超级管理员不能看审计（三员分立），越权 404
	if got := g.admin.do(http.MethodGet, "/api/v1/admin/audit-logs/traces/"+tr, "", false).Code; got != 404 {
		t.Fatalf("超级管理员应 404：%d", got)
	}
}

func TestTokenTruncationKeepsNewestUser(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	// 上下文窗口 1200、输出 1000 → 输入预算 300（下限为窗口的 1/4）
	g.c.Model.Update().Where(model.Name("ds-flash")).SetContextWindow(1200).SetMaxOutputTokens(1000).ExecX(ctx)
	id := newConv(t, g.admin, `{}`)
	send := fmt.Sprintf("/api/v1/conversations/%d/messages", id)
	for i := 0; i < 4; i++ {
		body := fmt.Sprintf(`{"content":%q,"model":"ds-flash"}`, fmt.Sprintf("第%d轮", i)+strings.Repeat("长", 100))
		if rec := g.admin.do(http.MethodPost, send, body, true); rec.Code != 200 {
			t.Fatalf("send %d %s", rec.Code, rec.Body)
		}
	}
	last := strings.Repeat("新", 500) // 单独就超过预算：仍须发送
	rec := g.admin.do(http.MethodPost, send, fmt.Sprintf(`{"content":%q,"model":"ds-flash"}`, last), true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"history_truncated":true`) {
		t.Fatalf("应提示历史已截断 %d %s", rec.Code, rec.Body)
	}
	msgs, _ := g.m.Requests[len(g.m.Requests)-1]["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("超预算时只保留最新一条用户消息：%d", len(msgs))
	}
	m0, _ := msgs[0].(map[string]any)
	if m0["role"] != "user" || m0["content"] != last {
		t.Fatalf("最新用户消息必须保留 %v", m0["role"])
	}
	r := g.c.AuditLog.Query().Where(auditlog.Action("chat.message.send"), auditlog.PhaseEQ(auditlog.PhaseResult)).
		Order(auditlog.ByID()).AllX(ctx)
	if d := r[len(r)-1].Detail; d["history_truncated"] != true || d["history_dropped"].(float64) < 1 {
		t.Fatalf("审计应记录截断 %v", d)
	}
}
