package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
)

func TestAuditDeniedLoginAndQuery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// 越权尝试被记录。
	m := f.loginAs(t, "member@x.local", "password123")
	if got := m.do(http.MethodGet, "/api/v1/admin/audit-logs", "", false).Code; got != 404 {
		t.Fatal(got)
	}
	if n := f.c.AuditLog.Query().Where(auditlog.Action("authz.denied"), auditlog.ActorEmail("member@x.local")).CountX(ctx); n != 1 {
		t.Fatalf("denied audit %d", n)
	}
	// 登录失败只记账号，不记密码。
	login(t, f.h, "ghost@x.local", "S3cretGuess!")
	rows := f.c.AuditLog.Query().Where(auditlog.ActorEmail("ghost@x.local")).AllX(ctx)
	if len(rows) == 0 {
		t.Fatal("failed login not audited")
	}
	for _, r := range f.c.AuditLog.Query().AllX(ctx) {
		if strings.Contains(r.ActorEmail+r.ResourceID+r.Outcome+jsonStr(r.Detail), "S3cretGuess") {
			t.Fatal("password leaked into audit")
		}
	}
	a := f.loginAs(t, "auditor@x.local", "password123")
	if rec := a.do(http.MethodGet, "/api/v1/admin/audit-logs?action=authz.denied", "", false); rec.Code != 200 || !strings.Contains(rec.Body.String(), "member@x.local") {
		t.Fatalf("list %d %s", rec.Code, rec.Body)
	}
	if rec := a.do(http.MethodGet, "/api/v1/admin/audit-logs/verify", "", false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("verify %d %s", rec.Code, rec.Body)
	}
	// FR-40 保留期状态：审计员可读，普通成员不可见。
	if rec := a.do(http.MethodGet, "/api/v1/admin/audit-logs/retention", "", false); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `"retention_days":365`) || !strings.Contains(rec.Body.String(), `"archives":[]`) {
		t.Fatalf("retention %d %s", rec.Code, rec.Body)
	}
	if got := m.do(http.MethodGet, "/api/v1/admin/audit-logs/retention", "", false).Code; got != 404 {
		t.Fatalf("member retention %d", got)
	}
	rec := a.do(http.MethodGet, "/api/v1/admin/audit-logs/export", "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "authz.denied") {
		t.Fatalf("export %d", rec.Code)
	}
	// 查看与导出本身被审计。
	for _, act := range []string{"audit.view", "audit.export", "audit.verify"} {
		if f.c.AuditLog.Query().Where(auditlog.Action(act)).CountX(ctx) == 0 {
			t.Errorf("%s not audited", act)
		}
	}
}
