package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/aigc"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

func TestAIGCSettingsAndExport(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	org := g.c.Organization.Query().OnlyX(ctx)
	g.addUser(t, "orgadmin@x.local", org, rbac.OrgAdmin, nil)
	member := g.c.User.Query().Where(user.Email("member@x.local")).OnlyX(ctx)
	mem := g.loginAs(t, "member@x.local", "password123")
	oa := g.loginAs(t, "orgadmin@x.local", "password123")

	// 组织管理员无 SettingsManage → 404；超管可读写
	if got := oa.do(http.MethodGet, "/api/v1/admin/aigc", "", false).Code; got != 404 {
		t.Fatalf("orgadmin aigc %d", got)
	}
	rec := g.admin.do(http.MethodGet, "/api/v1/admin/aigc", "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), aigc.DefaultBadge) {
		t.Fatalf("get %d %s", rec.Code, rec.Body)
	}
	if got := g.admin.do(http.MethodPut, "/api/v1/admin/aigc", `{"badge":"生成","suffix":"本内容由人工智能生成"}`, true).Code; got != 400 {
		t.Fatalf("bad badge %d", got)
	}
	if got := g.admin.do(http.MethodPut, "/api/v1/admin/aigc", `{"badge":"","suffix":"本内容由人工智能生成"}`, true).Code; got != 400 {
		t.Fatalf("empty badge %d", got)
	}
	if got := g.admin.do(http.MethodPut, "/api/v1/admin/aigc", `{"badge":"AI 生成","suffix":""}`, true).Code; got != 400 {
		t.Fatalf("empty suffix %d", got)
	}
	if got := g.admin.do(http.MethodPut, "/api/v1/admin/aigc", `{"badge":"AI 合成","suffix":"人工智能生成内容"}`, true).Code; got != 400 {
		t.Fatalf("phrase missing %d", got)
	}
	rec = g.admin.do(http.MethodPut, "/api/v1/admin/aigc", `{"badge":"AI 生成","suffix":"人工智能生成内容"}`, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "AI 生成") {
		t.Fatalf("put %d %s", rec.Code, rec.Body)
	}
	if n := g.c.AuditLog.Query().Where(auditlog.Action("aigc.settings.update")).CountX(ctx); n < 1 {
		t.Fatalf("settings audit %d", n)
	}

	rec = mem.do(http.MethodGet, "/api/v1/aigc", "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "AI 生成") {
		t.Fatalf("public %d %s", rec.Code, rec.Body)
	}

	cv := g.c.Conversation.Create().SetOrgID(org.ID).SetUserID(member.ID).SetTitle("测").SaveX(ctx)
	g.c.Message.Create().SetConversationID(cv.ID).SetRole(message.RoleUser).SetContent("问").SaveX(ctx)
	g.c.Message.Create().SetConversationID(cv.ID).SetRole(message.RoleAssistant).SetContent("答").
		SetRequestID("req-aigc-1").SetCreatedAt(time.Now()).SaveX(ctx)

	id := strconv.Itoa(cv.ID)
	rec = mem.do(http.MethodGet, "/api/v1/conversations/"+id, "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"aigc":true`) || !strings.Contains(rec.Body.String(), "AI 生成") {
		t.Fatalf("get conv %d %s", rec.Code, rec.Body)
	}
	if h := rec.Header().Get(aigc.HeaderName); !strings.Contains(h, "AIGC") || !strings.Contains(h, "req-aigc-1") {
		t.Fatalf("header %q", h)
	}

	rec = mem.do(http.MethodGet, "/api/v1/conversations/"+id+"/export?format=md", "", false)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "人工智能生成内容") || !strings.HasPrefix(body, "---\n") || !strings.Contains(body, "AIGC:") || !strings.Contains(body, "req-aigc-1") {
		t.Fatalf("export md %d %s", rec.Code, rec.Body)
	}
	if h := rec.Header().Get(aigc.HeaderName); !strings.Contains(h, "req-aigc-1") {
		t.Fatalf("export header %q", h)
	}
	rec = mem.do(http.MethodGet, "/api/v1/conversations/"+id+"/export?format=json", "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"AIGC"`) || !strings.Contains(rec.Body.String(), `"aigc"`) {
		t.Fatalf("export json %d %s", rec.Code, rec.Body)
	}
	if n := g.c.AuditLog.Query().Where(auditlog.Action("conversation.export")).CountX(ctx); n < 1 {
		t.Fatalf("export audit %d", n)
	}

	// 只能导出自己的会话：他人 404，未登录 401
	other := g.addUser(t, "other@x.local", org, rbac.Member, nil)
	oth := g.loginAs(t, "other@x.local", "password123")
	_ = other
	if got := oth.do(http.MethodGet, "/api/v1/conversations/"+id+"/export?format=md", "", false).Code; got != 404 {
		t.Fatalf("other export %d", got)
	}
	if got := g.admin.do(http.MethodGet, "/api/v1/conversations/"+id+"/export?format=md", "", false).Code; got != 404 {
		t.Fatalf("admin export others %d", got)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/"+id+"/export?format=md", nil)
	g.h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("anon export %d", rec.Code)
	}
}
