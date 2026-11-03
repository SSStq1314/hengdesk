package api_test

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
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/gateway/mockupstream"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

const upKey = "sk-mock-abcdef0123456789QRST"

type failModelCall struct{ audit.Writer }

func (w failModelCall) Write(ctx context.Context, e audit.Entry) error {
	if e.Action == "model.call" {
		return errors.New("audit store down")
	}
	return w.Writer.Write(ctx, e)
}

type gwEnv struct {
	*env
	m     *mockupstream.Server
	admin *client
	model string
}

func newGW(t *testing.T, failAudit bool) *gwEnv {
	m := mockupstream.New(upKey)
	t.Cleanup(m.Close)
	keys, _ := gateway.LoadKeyring("", strings.Repeat("11", 32))
	e := newEnv(t, func(d *api.Deps) {
		d.Gateway = &gateway.Service{DB: d.DB, Keys: keys, Client: &gateway.Client{}}
		if failAudit {
			d.Audit = failModelCall{d.Audit}
		}
	})
	g := &gwEnv{env: e, m: m, admin: e.loginAs(t, "admin@x.local", "password123")}
	rec := g.admin.do(http.MethodPost, "/api/v1/admin/model-providers",
		fmt.Sprintf(`{"name":"deepseek","dialect":"deepseek","base_url":%q,"api_key":%q}`, m.URL, upKey), true)
	if rec.Code != 201 {
		t.Fatalf("create provider %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), upKey) || !strings.Contains(rec.Body.String(), `"has_api_key":true`) {
		t.Fatalf("provider 响应泄露密钥或缺少 has_api_key: %s", rec.Body)
	}
	var pv struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &pv)
	rec = g.admin.do(http.MethodPost, "/api/v1/admin/models",
		fmt.Sprintf(`{"name":"ds-flash","provider_id":%d,"upstream_model":"deepseek-flash","thinking":"on"}`, pv.ID), true)
	if rec.Code != 201 {
		t.Fatalf("create model %d %s", rec.Code, rec.Body)
	}
	g.model = "ds-flash"
	return g
}

func TestModelAdminCRUDAndKeyNeverReturned(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	// 数据库里是密文
	p := g.c.ModelProvider.Query().OnlyX(ctx)
	if strings.Contains(string(p.APIKeyEnc), "0123456789") {
		t.Fatal("数据库里是明文")
	}
	for _, path := range []string{"/api/v1/admin/model-providers", "/api/v1/admin/models"} {
		rec := g.admin.do(http.MethodGet, path, "", false)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), upKey) || strings.Contains(rec.Body.String(), "api_key_enc") {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	// 非超级管理员一律 404
	org := g.c.Organization.Query().FirstX(ctx)
	g.addUser(t, "orgadmin@x.local", org, rbac.OrgAdmin, nil)
	g.addUser(t, "auditor@x.local", org, rbac.SecurityAuditor, nil)
	f := g.env
	for _, who := range []string{"member@x.local", "orgadmin@x.local", "auditor@x.local"} {
		cl := f.loginAs(t, who, "password123")
		if got := cl.do(http.MethodGet, "/api/v1/admin/model-providers", "", false).Code; got != 404 {
			t.Errorf("%s list %d", who, got)
		}
		if got := cl.do(http.MethodPost, "/api/v1/admin/models", `{}`, true).Code; got != 404 {
			t.Errorf("%s create %d", who, got)
		}
	}
	// 不存在 / 非法 id 404；有模型时删除 provider 409
	if got := g.admin.do(http.MethodPatch, "/api/v1/admin/model-providers/99999", `{"enabled":false}`, true).Code; got != 404 {
		t.Errorf("missing %d", got)
	}
	if got := g.admin.do(http.MethodPatch, "/api/v1/admin/model-providers/abc", `{}`, true).Code; got != 404 {
		t.Errorf("bad id %d", got)
	}
	if got := g.admin.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/model-providers/%d", p.ID), "", true).Code; got != 409 {
		t.Errorf("delete in use %d", got)
	}
	// 改名后密钥重新封装，仍可调用
	if rec := g.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/model-providers/%d", p.ID), `{"name":"ds2"}`, true); rec.Code != 200 {
		t.Fatalf("rename %d %s", rec.Code, rec.Body)
	}
	if rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", `{"model":"ds-flash","messages":[{"role":"user","content":"hi"}]}`, true); rec.Code != 200 {
		t.Fatalf("chat after rename %d %s", rec.Code, rec.Body)
	}
	// 审计里没有密钥
	for _, r := range g.c.AuditLog.Query().AllX(ctx) {
		b, _ := json.Marshal(r.Detail)
		if strings.Contains(string(b), upKey) || strings.Contains(string(b), "QRST") {
			t.Fatalf("审计泄露密钥: %s %s", r.Action, b)
		}
	}
	if g.c.AuditLog.Query().Where(auditlog.Action("model.provider.create")).CountX(ctx) == 0 {
		t.Fatal("缺少 model.provider.create 审计")
	}
}

func TestChatNonStreamSSEAndACL(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	body := `{"model":"ds-flash","messages":[{"role":"user","content":"查工单"}],"tools":[{"type":"function","function":{"name":"get_ticket","parameters":{"type":"object"}}}],"tool_choice":"required"}`
	rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", body, true)
	if rec.Code != 200 {
		t.Fatalf("chat %d %s", rec.Code, rec.Body)
	}
	var res gateway.ChatResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Message.ToolCalls) != 1 || res.Message.ReasoningContent == nil || len(res.Warnings) == 0 || res.Usage.TotalTokens == 0 {
		t.Fatalf("响应: %s", rec.Body)
	}
	// 第二轮：回传 assistant 消息（含 reasoning_content）
	asst, _ := json.Marshal(res.Message)
	body2 := fmt.Sprintf(`{"model":"ds-flash","stream":true,"messages":[{"role":"user","content":"查工单"},%s,{"role":"tool","tool_call_id":%q,"content":"{\"status\":\"处理中\"}"}],"tools":[{"type":"function","function":{"name":"get_ticket","parameters":{"type":"object"}}}]}`, asst, res.Message.ToolCalls[0].ID)
	rec = g.admin.do(http.MethodPost, "/api/v1/chat/completions", body2, true)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse %d %s", rec.Code, rec.Body)
	}
	s := rec.Body.String()
	if !strings.Contains(s, "event: delta") || !strings.Contains(s, "event: done") || !strings.Contains(s, "处理中") {
		t.Fatalf("sse body: %s", s)
	}
	// 拒绝策略
	g.c.Model.Update().SetToolChoicePolicy("reject").ExecX(ctx)
	rec = g.admin.do(http.MethodPost, "/api/v1/chat/completions", body, true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "tool_choice_unsupported") {
		t.Fatalf("reject %d %s", rec.Code, rec.Body)
	}
	// 普通成员无 ACL：404；不存在的模型同样 404
	mem := g.loginAs(t, "member@x.local", "password123")
	chat := `{"model":"ds-flash","messages":[{"role":"user","content":"hi"}]}`
	if got := mem.do(http.MethodPost, "/api/v1/chat/completions", chat, true).Code; got != 404 {
		t.Fatalf("no acl %d", got)
	}
	if got := mem.do(http.MethodPost, "/api/v1/chat/completions", `{"model":"nope","messages":[{"role":"user","content":"hi"}]}`, true).Code; got != 404 {
		t.Fatalf("missing model %d", got)
	}
	// 授予 use 后可用
	memID := atoi(g.uid(t, "member@x.local"))
	mid := g.c.Model.Query().OnlyX(ctx).ID
	if rec := g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/acl", mid), fmt.Sprintf(`{"subject_type":"user","subject_id":%d}`, memID), true); rec.Code != 204 {
		t.Fatalf("grant %d %s", rec.Code, rec.Body)
	}
	if rec := mem.do(http.MethodPost, "/api/v1/chat/completions", chat, true); rec.Code != 200 {
		t.Fatalf("with acl %d %s", rec.Code, rec.Body)
	}
	// 每次调用都有 model.call 审计，且不含内容
	calls := g.c.AuditLog.Query().Where(auditlog.Action("model.call")).AllX(ctx)
	if len(calls) < 3 {
		t.Fatalf("model.call 审计条数 %d", len(calls))
	}
	for _, r := range calls {
		b, _ := json.Marshal(r.Detail)
		if strings.Contains(string(b), "查工单") {
			t.Fatalf("审计不应记录内容: %s", b)
		}
	}
}

func TestChatUpstream401Redacted(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	p := g.c.ModelProvider.Query().OnlyX(ctx)
	bad := "sk-wrong-0000000000009876"
	if rec := g.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/model-providers/%d", p.ID), fmt.Sprintf(`{"api_key":%q}`, bad), true); rec.Code != 200 {
		t.Fatalf("patch %d", rec.Code)
	}
	for _, stream := range []string{"false", "true"} {
		rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", `{"model":"ds-flash","stream":`+stream+`,"messages":[{"role":"user","content":"hi"}]}`, true)
		if rec.Code != 502 || !strings.Contains(rec.Body.String(), "upstream_auth_failed") || strings.Contains(rec.Body.String(), "9876") {
			t.Fatalf("401 stream=%s: %d %s", stream, rec.Code, rec.Body)
		}
	}
	for _, r := range g.c.AuditLog.Query().AllX(ctx) {
		b, _ := json.Marshal(r.Detail)
		if strings.Contains(string(b), "9876") || strings.Contains(string(b), bad) {
			t.Fatalf("审计泄露密钥片段: %s", b)
		}
	}
	g.m.FailStatus = 503
	if rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", `{"model":"ds-flash","messages":[{"role":"user","content":"hi"}]}`, true); rec.Code != 502 {
		t.Fatalf("5xx %d", rec.Code)
	}
}

func TestChatAuditFailClosed(t *testing.T) {
	g := newGW(t, true)
	before := g.m.Count()
	rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", `{"model":"ds-flash","messages":[{"role":"user","content":"hi"}]}`, true)
	if rec.Code != 503 {
		t.Fatalf("审计失败应拒绝: %d %s", rec.Code, rec.Body)
	}
	if g.m.Count() != before {
		t.Fatal("审计失败时不应调用上游")
	}
}
