package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/egress"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/egresslog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/egressrule"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

const chatBody = `{"model":"ds-flash","messages":[{"role":"user","content":"hi"}]}`

// 网关端到端经过出站控制：放行时有出站日志（无密钥），删掉白名单后 403 egress_denied 并写审计。
func TestGatewayEgressEndToEnd(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	if rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", chatBody, true); rec.Code != 200 {
		t.Fatalf("chat %d %s", rec.Code, rec.Body)
	}
	logs := g.c.EgressLog.Query().Where(egresslog.DecisionEQ(egresslog.DecisionAllow), egresslog.Module("model.chat")).AllX(ctx)
	if len(logs) != 1 || logs[0].Status != 200 || logs[0].ActorEmail != "admin@x.local" || logs[0].UserID == nil ||
		logs[0].Path != "/chat/completions" || logs[0].BytesIn == 0 || logs[0].BytesOut == 0 || logs[0].IP != "127.0.0.1" {
		t.Fatalf("%+v", logs)
	}
	// 管理员查看出站日志：不含上游密钥
	rec := g.admin.do(http.MethodGet, "/api/v1/admin/egress/logs", "", false)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), upKey) || strings.Contains(rec.Body.String(), "Bearer") {
		t.Fatalf("logs %d %s", rec.Code, rec.Body)
	}
	// 删除放行条目 → 拒绝
	r := g.c.EgressRule.Query().OnlyX(ctx)
	if rec := g.admin.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/egress/rules/%d", r.ID), "", true); rec.Code != 204 {
		t.Fatalf("delete %d %s", rec.Code, rec.Body)
	}
	before := g.m.Count()
	rec = g.admin.do(http.MethodPost, "/api/v1/chat/completions", chatBody, true)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "egress_denied") || !strings.Contains(rec.Body.String(), "出站控制") {
		t.Fatalf("want egress_denied: %d %s", rec.Code, rec.Body)
	}
	if g.m.Count() != before {
		t.Fatal("上游不应收到请求")
	}
	if n := g.c.AuditLog.Query().Where(auditlog.Action("egress.deny")).CountX(ctx); n != 1 {
		t.Fatalf("deny audit %d", n)
	}
	d := g.c.EgressLog.Query().Where(egresslog.DecisionEQ(egresslog.DecisionDeny)).OnlyX(ctx)
	if d.Reason != egress.ReasonEmpty || d.Host != "127.0.0.1" {
		t.Fatalf("%+v", d)
	}
	// 模型测试连接同样被拦截
	if rec := g.admin.do(http.MethodPost, "/api/v1/admin/model-providers/"+fmt.Sprint(g.c.ModelProvider.Query().OnlyX(ctx).ID)+"/test", "", true); !strings.Contains(rec.Body.String(), "egress_denied") {
		t.Fatalf("provider test %d %s", rec.Code, rec.Body)
	}
}

func TestEgressRulesAdmin(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	org := g.c.Organization.Query().FirstX(ctx)
	g.addUser(t, "orgadmin@x.local", org, rbac.OrgAdmin, nil)
	g.addUser(t, "auditor@x.local", org, rbac.SecurityAuditor, nil)
	// 越权 404；审计员能只读出站日志
	for _, who := range []string{"member@x.local", "orgadmin@x.local", "auditor@x.local"} {
		cl := g.loginAs(t, who, "password123")
		for _, rq := range [][3]string{{"GET", "/api/v1/admin/egress/rules", ""}, {"POST", "/api/v1/admin/egress/rules", `{"kind":"host","value":"a.com"}`},
			{"DELETE", "/api/v1/admin/egress/rules/1", ""}, {"POST", "/api/v1/admin/egress/check", `{"url":"https://a.com"}`}} {
			if got := cl.do(rq[0], rq[1], rq[2], rq[0] != "GET").Code; got != 404 {
				t.Errorf("%s %s %s = %d", who, rq[0], rq[1], got)
			}
		}
		want := 404
		if who == "auditor@x.local" {
			want = 200
		}
		if got := cl.do("GET", "/api/v1/admin/egress/logs", "", false).Code; got != want {
			t.Errorf("%s logs %d", who, got)
		}
	}
	for _, body := range []string{`{"kind":"cidr","value":"0.0.0.0/0"}`, `{"kind":"host","value":"bad host"}`, `{"kind":"wildcard","value":"*.com"}`, `{"kind":"host","value":"a.com","ports":"99999"}`} {
		if rec := g.admin.do("POST", "/api/v1/admin/egress/rules", body, true); rec.Code != 400 {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	rec := g.admin.do("POST", "/api/v1/admin/egress/rules", `{"kind":"wildcard","value":"*.DeepSeek.com","ports":"443","note":"官方"}`, true)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"value":"*.deepseek.com"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := g.admin.do("POST", "/api/v1/admin/egress/rules", `{"kind":"wildcard","value":"*.deepseek.com","ports":"443"}`, true); rec.Code != 409 {
		t.Fatalf("dup %d", rec.Code)
	}
	// 显式放行内网网段：额外记一条 egress.private_allow
	rec = g.admin.do("POST", "/api/v1/admin/egress/rules", `{"kind":"cidr","value":"10.8.0.0/16","ports":"8000","note":"vLLM"}`, true)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"private":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if n := g.c.AuditLog.Query().Where(auditlog.Action("egress.private_allow")).CountX(ctx); n != 1 {
		t.Fatalf("private_allow %d", n)
	}
	if n := g.c.AuditLog.Query().Where(auditlog.Action("egress.rule.create")).CountX(ctx); n != 2 {
		t.Fatalf("create audit %d", n)
	}
	var v struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if rec := g.admin.do("PATCH", fmt.Sprintf("/api/v1/admin/egress/rules/%d", v.ID), `{"kind":"cidr","value":"10.8.0.0/16","ports":"8000,8001"}`, true); rec.Code != 200 {
		t.Fatalf("patch %d %s", rec.Code, rec.Body)
	}
	if rec := g.admin.do("PATCH", "/api/v1/admin/egress/rules/99999", `{"kind":"host","value":"a.com"}`, true); rec.Code != 404 {
		t.Fatalf("patch missing %d", rec.Code)
	}
	rec = g.admin.do("POST", "/api/v1/admin/egress/check", `{"url":"http://169.254.169.254/latest"}`, true)
	if !strings.Contains(rec.Body.String(), `"allowed":false`) || !strings.Contains(rec.Body.String(), egress.ReasonMetadata) {
		t.Fatalf("check %s", rec.Body)
	}
}

// 新建上游：只提示是否在白名单，不自动添加。
func TestProviderEgressHint(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	n := g.c.EgressRule.Query().CountX(ctx)
	rec := g.admin.do(http.MethodPost, "/api/v1/admin/model-providers", `{"name":"other","base_url":"http://10.20.30.40:8000/v1"}`, true)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"allowed":false`) || !strings.Contains(rec.Body.String(), egress.ReasonInternal) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if g.c.EgressRule.Query().CountX(ctx) != n {
		t.Fatal("不应自动添加白名单")
	}
	rec = g.admin.do(http.MethodPost, "/api/v1/admin/model-providers", `{"name":"local","base_url":"http://127.0.0.1:1/v1"}`, true)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"egress":{"allowed":true}`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// 审计不可用：拒绝的出站同样拒绝，且日志标记 audit_unavailable。
func TestEgressDenyAuditFailure(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	g.c.EgressRule.Delete().ExecX(ctx)
	cl := &egress.Client{Policy: &egress.DBPolicy{DB: g.c}, Sink: egress.DBSink{DB: g.c}, Audit: failWriter{}}
	resp, err := cl.HTTP().Get(g.m.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("want deny")
	}
	d := g.c.EgressLog.Query().OnlyX(ctx)
	if d.Decision != egresslog.DecisionDeny || d.Error != egress.ReasonAuditFailed {
		t.Fatalf("%+v", d)
	}
}

type failWriter struct{}

func (failWriter) Write(context.Context, audit.Entry) error { return fmt.Errorf("down") }

// 升级迁移：已有上游主机写入白名单（内网另写 IP 条目），每条审计，只执行一次。
func TestEgressUpgradeSeed(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	w := audit.EntWriter{C: c}
	if n, err := egress.Seed(ctx, c, w, nil, ""); err != nil || n != 0 {
		t.Fatalf("fresh install %d %v", n, err)
	}
	if c.EgressRule.Query().CountX(ctx) != 0 {
		t.Fatal("全新安装白名单应为空")
	}
	c2 := storetest.NewTestClient(t)
	c2.ModelProvider.Create().SetName("ds").SetBaseURL("https://api.deepseek.com/v1").ExecX(ctx)
	c2.ModelProvider.Create().SetName("ollama").SetBaseURL("http://127.0.0.1:11434/v1").ExecX(ctx)
	c2.ModelProvider.Create().SetName("meta").SetBaseURL("http://169.254.169.254/").ExecX(ctx)
	w2 := audit.EntWriter{C: c2}
	n, err := egress.Seed(ctx, c2, w2, fakeResolver{}, "https://sso.example.com/realms/x")
	if err != nil || n != 3 {
		t.Fatalf("seed %d %v", n, err)
	}
	if c2.EgressRule.Query().Where(egressrule.Value("169.254.169.254")).ExistX(ctx) {
		t.Fatal("元数据地址不应写入")
	}
	if !c2.EgressRule.Query().Where(egressrule.Value("127.0.0.1"), egressrule.Private(true), egressrule.SourceEQ(egressrule.SourceUpgrade)).ExistX(ctx) ||
		!c2.EgressRule.Query().Where(egressrule.Value("sso.example.com"), egressrule.SourceEQ(egressrule.SourceOidc)).ExistX(ctx) {
		t.Fatalf("%+v", c2.EgressRule.Query().AllX(ctx))
	}
	if a := c2.AuditLog.Query().Where(auditlog.Action("egress.rule.seed")).CountX(ctx); a != 3 {
		t.Fatalf("seed audit %d", a)
	}
	if n, _ := egress.Seed(ctx, c2, w2, fakeResolver{}, ""); n != 0 {
		t.Fatal("只执行一次")
	}
}

type fakeResolver struct{}

func (fakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("1.2.3.4")}, nil
}
