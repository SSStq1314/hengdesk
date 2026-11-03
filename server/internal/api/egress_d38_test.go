package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/egress"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/egresslog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/egressrule"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

// failStartSink 放行日志写不进去（Start 失败），最终结果照常写库。
type failStartSink struct{ egress.Sink }

func (failStartSink) Start(context.Context, egress.Record) (int, error) {
	return 0, errors.New("log down")
}

// D-38 第 9 条：放行日志写失败时，只有声明了审计 fail-closed 的模型网关对话可以继续，测试连接等未声明的调用方拒绝。
func TestEgressAllowLogFailureByCaller(t *testing.T) {
	g := newGW(t, false, func(d *api.Deps) { d.Egress.Sink = failStartSink{d.Egress.Sink} })
	ctx := context.Background()
	if rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", chatBody, true); rec.Code != 200 {
		t.Fatalf("模型网关已声明，应放行：%d %s", rec.Code, rec.Body)
	}
	pid := g.c.ModelProvider.Query().OnlyX(ctx).ID
	rec := g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/model-providers/%d/test", pid), "", true)
	if !strings.Contains(rec.Body.String(), "egress_denied") || !strings.Contains(rec.Body.String(), "出站日志写入失败") {
		t.Fatalf("未声明的调用方应被拒绝：%d %s", rec.Code, rec.Body)
	}
	if n := g.c.AuditLog.Query().Where(auditlog.Action("egress.deny")).CountX(ctx); n != 1 {
		t.Fatalf("deny audit %d", n)
	}
}

// 只有 chat.go（模型网关对话，出站前已写 model.call attempt 审计）可以声明审计 fail-closed。
func TestOnlyGatewayDeclaresAuditedFailClosed(t *testing.T) {
	var hits []string
	_ = filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, _ := os.ReadFile(p)
		if (strings.Contains(string(b), "egressCtxAudited(") && p != "chat.go" && p != "egress_ctx.go") ||
			(strings.Contains(string(b), "AuditedFailClosed") && p != "egress_ctx.go") {
			hits = append(hits, p)
		}
		return nil
	})
	if len(hits) > 0 {
		t.Fatalf("以下文件声明了审计 fail-closed，需先确认出站前已写审计并同步 D-38 第 9 条的清单：%v", hits)
	}
}

// failEgressLogAudit 出站日志的敏感读取审计写不进去。
type failEgressLogAudit struct{ audit.Writer }

func (f failEgressLogAudit) Write(ctx context.Context, e audit.Entry) error {
	if strings.HasPrefix(e.Action, "egress.log.") {
		return errors.New("down")
	}
	return f.Writer.Write(ctx, e)
}

// D-38 第 10 条：查看 / 筛选 / 导出出站日志按敏感读取记审计（筛选条件、行数）；审计写不进去就拒绝。
func TestEgressLogSensitiveRead(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	org := g.c.Organization.Query().FirstX(ctx)
	g.addUser(t, "auditor@x.local", org, rbac.SecurityAuditor, nil)
	if rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", chatBody, true); rec.Code != 200 {
		t.Fatalf("chat %d", rec.Code)
	}
	au := g.loginAs(t, "auditor@x.local", "password123")
	if rec := au.do("GET", "/api/v1/admin/egress/logs", "", false); rec.Code != 200 {
		t.Fatalf("view %d", rec.Code)
	}
	if rec := au.do("GET", "/api/v1/admin/egress/logs?decision=allow&host=127.0.0.1", "", false); rec.Code != 200 {
		t.Fatalf("filter %d", rec.Code)
	}
	rec := au.do("GET", "/api/v1/admin/egress/logs/export?module=model.chat", "", false)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") || !strings.Contains(rec.Body.String(), "model.chat") ||
		strings.Contains(rec.Body.String(), upKey) {
		t.Fatalf("export %d %s", rec.Code, rec.Body)
	}
	for action, want := range map[string]map[string]any{
		"egress.log.view":   {"rows": float64(1)},
		"egress.log.filter": {"rows": float64(1)},
		"egress.log.export": {"rows": float64(1)},
	} {
		e, err := g.c.AuditLog.Query().Where(auditlog.Action(action)).Only(ctx)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if e.ActorEmail != "auditor@x.local" || e.Detail["rows"] != want["rows"] || e.Detail["filters"] == nil {
			t.Fatalf("%s: %+v", action, e)
		}
	}
	f := g.c.AuditLog.Query().Where(auditlog.Action("egress.log.filter")).OnlyX(ctx).Detail["filters"].(map[string]any)
	if f["decision"] != "allow" || f["host"] != "127.0.0.1" {
		t.Fatalf("filters %+v", f)
	}
	// 越权 404
	member := g.loginAs(t, "member@x.local", "password123")
	if got := member.do("GET", "/api/v1/admin/egress/logs/export", "", false).Code; got != 404 {
		t.Fatalf("member export %d", got)
	}

	g2 := newGW(t, false, func(d *api.Deps) { d.Audit = failEgressLogAudit{d.Audit} })
	for _, u := range []string{"/api/v1/admin/egress/logs", "/api/v1/admin/egress/logs?decision=deny", "/api/v1/admin/egress/logs/export", "/api/v1/admin/egress/blocked"} {
		rec := g2.admin.do("GET", u, "", false)
		if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "items") || strings.Contains(rec.Body.String(), "decision,") {
			t.Fatalf("%s: 审计失败应拒绝，got %d %s", u, rec.Code, rec.Body)
		}
	}
}

// D-38 第 1 条：自动添加的条目标记「待复核」，管理员复核一次（写审计）。第 3 条：端口 * 标记风险。
func TestEgressRuleReview(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	r := g.c.EgressRule.Create().SetKind(egressrule.KindHost).SetValue("api.deepseek.com").SetPorts("443").SetSource(egressrule.SourceUpgrade).SaveX(ctx)
	var list struct {
		Items []struct {
			ID          int  `json:"id"`
			NeedsReview bool `json:"needs_review"`
			AnyPort     bool `json:"any_port"`
		} `json:"items"`
		PendingReview int `json:"pending_review"`
		DefaultPort   int `json:"default_port"`
	}
	_ = json.Unmarshal(g.admin.do("GET", "/api/v1/admin/egress/rules", "", false).Body.Bytes(), &list)
	if list.PendingReview != 1 || list.DefaultPort != 443 {
		t.Fatalf("%+v", list)
	}
	for _, it := range list.Items {
		if it.ID == r.ID && !it.NeedsReview || it.ID != r.ID && (it.NeedsReview || !it.AnyPort) {
			t.Fatalf("%+v", list.Items)
		}
	}
	if rec := g.admin.do("POST", fmt.Sprintf("/api/v1/admin/egress/rules/%d/review", r.ID), "", true); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"needs_review":false`) {
		t.Fatalf("review %d %s", rec.Code, rec.Body)
	}
	if n := g.c.AuditLog.Query().Where(auditlog.Action("egress.rule.review")).CountX(ctx); n != 1 {
		t.Fatalf("review audit %d", n)
	}
	manual := g.c.EgressRule.Query().Where(egressrule.SourceEQ(egressrule.SourceManual)).FirstX(ctx)
	if rec := g.admin.do("POST", fmt.Sprintf("/api/v1/admin/egress/rules/%d/review", manual.ID), "", true); rec.Code != 400 {
		t.Fatalf("manual review %d", rec.Code)
	}
	if rec := g.admin.do("POST", "/api/v1/admin/egress/rules/99999/review", "", true); rec.Code != 404 {
		t.Fatalf("missing %d", rec.Code)
	}
	member := g.loginAs(t, "member@x.local", "password123")
	if got := member.do("POST", fmt.Sprintf("/api/v1/admin/egress/rules/%d/review", r.ID), "", true).Code; got != 404 {
		t.Fatalf("member review %d", got)
	}
	// 端口 * 可保存；与具体端口混用 400
	if rec := g.admin.do("POST", "/api/v1/admin/egress/rules", `{"kind":"host","value":"any.example.com","ports":"*"}`, true); rec.Code != 201 || !strings.Contains(rec.Body.String(), `"any_port":true`) {
		t.Fatalf("* %d %s", rec.Code, rec.Body)
	}
	if rec := g.admin.do("POST", "/api/v1/admin/egress/rules", `{"kind":"host","value":"mix.example.com","ports":"443,*"}`, true); rec.Code != 400 {
		t.Fatalf("mix %d", rec.Code)
	}
}

// D-38 第 3 条升级迁移：空端口的自动条目改为上游 URL 实际端口，手工条目改为 *，逐条审计，只执行一次。
func TestEgressMigratePorts(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	c.ModelProvider.Create().SetName("vllm").SetBaseURL("http://vllm.corp:8000/v1").ExecX(ctx)
	c.EgressRule.Create().SetKind(egressrule.KindHost).SetValue("vllm.corp").SetSource(egressrule.SourceUpgrade).ExecX(ctx)
	c.EgressRule.Create().SetKind(egressrule.KindHost).SetValue("gone.example.com").SetSource(egressrule.SourceUpgrade).ExecX(ctx)
	c.EgressRule.Create().SetKind(egressrule.KindHost).SetValue("manual.example.com").ExecX(ctx)
	c.EgressRule.Create().SetKind(egressrule.KindHost).SetValue("kept.example.com").SetPorts("8443").ExecX(ctx)
	w := audit.EntWriter{C: c}
	n, err := egress.MigratePorts(ctx, c, w, fakeResolver{}, "")
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	want := map[string]string{"vllm.corp": "8000", "gone.example.com": "443", "manual.example.com": "*", "kept.example.com": "8443"}
	for _, r := range c.EgressRule.Query().AllX(ctx) {
		if want[r.Value] != r.Ports {
			t.Errorf("%s ports=%q want %q", r.Value, r.Ports, want[r.Value])
		}
	}
	if a := c.AuditLog.Query().Where(auditlog.Action("egress.rule.ports_migrate")).CountX(ctx); a != 3 {
		t.Fatalf("audit %d", a)
	}
	c.EgressRule.Create().SetKind(egressrule.KindHost).SetValue("new.example.com").ExecX(ctx)
	if n, _ := egress.MigratePorts(ctx, c, w, fakeResolver{}, ""); n != 0 {
		t.Fatal("只执行一次；之后新建的空端口条目按新语义仅 443")
	}
}

// D-39 第 1 条：#31 改成 * 的手工条目收窄为 443（上游引用的主机用实际端口）、标记待复核、逐条审计；
// 之后被管理员改过的、本来就是 * 的手工条目不动；只执行一次。
func TestEgressMigrateD39Ports(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	c.ModelProvider.Create().SetName("vllm").SetBaseURL("http://vllm.corp:9000/v1").ExecX(ctx)
	for _, v := range []string{"plain.example.com", "vllm.corp", "edited.example.com", "dup.example.com"} {
		c.EgressRule.Create().SetKind(egressrule.KindHost).SetValue(v).ExecX(ctx)
	}
	w := audit.EntWriter{C: c}
	if _, err := egress.MigratePorts(ctx, c, w, fakeResolver{}, ""); err != nil {
		t.Fatal(err)
	}
	// 用户自己建的 * 条目、#31 之后已存在的 443 同名条目、#31 之后管理员编辑过的条目
	own := c.EgressRule.Create().SetKind(egressrule.KindHost).SetValue("own.example.com").SetPorts("*").SaveX(ctx)
	c.EgressRule.Create().SetKind(egressrule.KindHost).SetValue("dup.example.com").SetPorts("443").ExecX(ctx)
	ed := c.EgressRule.Query().Where(egressrule.Value("edited.example.com")).OnlyX(ctx)
	time.Sleep(5 * time.Millisecond)
	if err := w.Write(ctx, audit.Entry{ActorEmail: "admin@x.local", Action: "egress.rule.update", ResourceType: "egress_rule",
		ResourceID: strconv.Itoa(ed.ID), Phase: "result", Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	n, err := egress.MigrateD39Ports(ctx, c, w, fakeResolver{}, "")
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	want := map[string]string{"plain.example.com": "443", "vllm.corp": "9000", "edited.example.com": "*", "own.example.com": "*"}
	for _, r := range c.EgressRule.Query().AllX(ctx) {
		if r.Value == "dup.example.com" {
			if r.Ports != "443" || !r.ReviewRequired {
				t.Errorf("dup 应合并为 443 并待复核：%+v", r)
			}
			continue
		}
		if want[r.Value] != r.Ports {
			t.Errorf("%s ports=%q want %q", r.Value, r.Ports, want[r.Value])
		}
		narrowed := r.Value == "plain.example.com" || r.Value == "vllm.corp"
		if r.ReviewRequired != narrowed || (narrowed && r.ReviewedAt != nil) {
			t.Errorf("%s review_required=%v", r.Value, r.ReviewRequired)
		}
	}
	if n := c.EgressRule.Query().Where(egressrule.Value("dup.example.com")).CountX(ctx); n != 1 {
		t.Fatalf("dup 条目数 %d", n)
	}
	if a := c.AuditLog.Query().Where(auditlog.Action("egress.rule.ports_migrate")).AllX(ctx); len(a) != 4+3 {
		t.Fatalf("audit %d", len(a))
	}
	_ = own
	if n, _ := egress.MigrateD39Ports(ctx, c, w, fakeResolver{}, ""); n != 0 {
		t.Fatal("只执行一次")
	}
}

// D-39 第 2 条：导出分批流式输出（跨多个批次），超过上限截断并在审计里记录匹配总数。
func TestEgressExportStreamed(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	for i := 0; i < 2500; i++ {
		g.c.EgressLog.Create().SetHost(fmt.Sprintf("h%d.example.com", i)).SetPort(443).SetDecision(egresslog.DecisionDeny).SetReason("not_allowed").ExecX(ctx)
	}
	rec := g.admin.do("GET", "/api/v1/admin/egress/logs/export", "", false)
	if rec.Code != 200 || rec.Header().Get("X-Export-Truncated") != "" {
		t.Fatalf("export %d", rec.Code)
	}
	if n := strings.Count(rec.Body.String(), "\n"); n != 2501 {
		t.Fatalf("行数 %d", n)
	}
	defer api.SetEgressExportMax(1200)()
	rec = g.admin.do("GET", "/api/v1/admin/egress/logs/export", "", false)
	if rec.Code != 200 || rec.Header().Get("X-Export-Truncated") != "true" || strings.Count(rec.Body.String(), "\n") != 1201 ||
		!strings.Contains(rec.Body.String(), "h2499.example.com") || strings.Contains(rec.Body.String(), "h1299.example.com,") {
		t.Fatalf("截断导出 %d lines=%d", rec.Code, strings.Count(rec.Body.String(), "\n"))
	}
	e := g.c.AuditLog.Query().Where(auditlog.Action("egress.log.export")).Order(ent.Desc(auditlog.FieldID)).FirstX(ctx)
	if e.Detail["rows"] != float64(1200) || e.Detail["matched"] != float64(2500) || e.Detail["truncated"] != true {
		t.Fatalf("audit %+v", e.Detail)
	}
	// 被拦截的出站请求列表：按主机聚合
	rec = g.admin.do("GET", "/api/v1/admin/egress/blocked", "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"host":"h2499.example.com"`) {
		t.Fatalf("blocked %d", rec.Code)
	}
	if g.c.AuditLog.Query().Where(auditlog.Action("egress.log.blocked")).CountX(ctx) != 1 {
		t.Fatal("blocked 应记敏感读取审计")
	}
	member := g.loginAs(t, "member@x.local", "password123")
	if got := member.do("GET", "/api/v1/admin/egress/blocked", "", false).Code; got != 404 {
		t.Fatalf("member blocked %d", got)
	}
}
