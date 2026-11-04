package api_test

// M1 第五块 PR2：工具治理（高危不可自动、待复核须确认、下线 / 服务器停用不可启用、审批人校验）、
// 工具 ACL（成员 / 部门含下级 / 角色）、员工可用工具列表、越权 404 / 未登录 401。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	"github.com/SSStq1314/hengdesk/server/internal/ent/tool"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

type adminTool struct {
	mcpToolResp
	ID                 int
	ApproverIDs        []int `json:"approver_ids"`
	ApprovalTimeoutSec int   `json:"approval_timeout_sec"`
	GrantCount         int   `json:"grant_count"`
	ServerEnabled      bool  `json:"server_enabled"`
}

// setupTools 登记并发现，返回 name→工具 ID。
func setupTools(t *testing.T, e *mcpEnv) (int, map[string]int) {
	t.Helper()
	s := e.register(t, "streamable_http", "/mcp")
	if code, _, body := e.discover(t, s.ID); code != 200 {
		t.Fatalf("discover %d %s", code, body)
	}
	ids := map[string]int{}
	for _, tl := range e.c.Tool.Query().Where(tool.ServerID(s.ID)).AllX(context.Background()) {
		ids[tl.Name] = tl.ID
	}
	return s.ID, ids
}

func (e *mcpEnv) patchTool(id int, body string) (int, adminTool, string) {
	rec := e.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/tools/%d", id), body, true)
	var v adminTool
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return rec.Code, v, rec.Body.String()
}

func myTools(t *testing.T, cl *client) []string {
	t.Helper()
	rec := cl.do(http.MethodGet, "/api/v1/tools", "", false)
	if rec.Code != 200 {
		t.Fatalf("my tools %d", rec.Code)
	}
	var v struct {
		Items []struct {
			Name     string `json:"name"`
			ExecMode string `json:"exec_mode"`
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	out := []string{}
	for _, it := range v.Items {
		out = append(out, it.Name+"/"+it.ExecMode)
	}
	return out
}

func TestToolGovernance(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	sid, ids := setupTools(t, e)
	ct := ids["create_ticket"]

	// 高危不能自动
	if code, _, _ := e.patchTool(ct, `{"risk_level":"high_risk","exec_mode":"auto"}`); code != 400 {
		t.Fatalf("high+auto %d", code)
	}
	if code, _, _ := e.patchTool(ct, `{"risk_level":"high_risk"}`); code != 200 {
		t.Fatal("set high")
	}
	if code, _, _ := e.patchTool(ct, `{"exec_mode":"auto"}`); code != 400 {
		t.Fatal("auto on high must fail")
	}
	for _, b := range []string{`{"exec_mode":"x"}`, `{"risk_level":"x"}`, `{"approval_timeout_sec":10}`,
		`{"approval_timeout_sec":9999999}`, `{"approver_ids":[99999]}`} {
		if code, _, _ := e.patchTool(ct, b); code != 400 {
			t.Errorf("%s: %d", b, code)
		}
	}
	// 其他组织的用户不能当审批人
	other := e.c.Organization.Create().SetName("别家").SetSlug("other").SaveX(ctx)
	ou := e.addUser(t, "o@x.local", other, "", nil)
	if code, _, _ := e.patchTool(ct, fmt.Sprintf(`{"approver_ids":[%d]}`, ou.ID)); code != 400 {
		t.Fatal("cross-org approver")
	}
	admin := e.c.User.Query().Where().FirstX(ctx)
	code, v, body := e.patchTool(ct, fmt.Sprintf(`{"enabled":true,"exec_mode":"require_approval","approver_ids":[%d,%d],"approval_timeout_sec":600}`, admin.ID, admin.ID))
	if code != 200 || !v.Enabled || len(v.ApproverIDs) != 1 || v.ApprovalTimeoutSec != 600 || !v.ServerEnabled {
		t.Fatalf("enable %d %s", code, body)
	}
	if n := e.c.AuditLog.Query().Where(auditlog.Action("tool.update")).CountX(ctx); n != 2 {
		t.Fatalf("tool.update audit %d", n)
	}

	// schema 变化 → 自动禁用 + 待复核；未确认不能启用，确认后可启用
	e.mock.SetTools(true)
	e.discover(t, sid)
	if code, _, _ := e.patchTool(ct, `{"enabled":true}`); code != 409 {
		t.Fatalf("review needed: %d", code)
	}
	if code, v, _ := e.patchTool(ct, `{"enabled":true,"confirm_review":true}`); code != 200 || !v.Enabled || v.NeedsReview {
		t.Fatalf("confirm %d %+v", code, v)
	}
	// 下线的工具不能启用
	qt := ids["query_ticket"]
	if code, _, _ := e.patchTool(qt, `{"enabled":true,"confirm_review":true}`); code != 409 {
		t.Fatalf("removed enable %d", code)
	}
	// 服务器停用后不能启用工具
	e.mock.SetTools(false)
	e.discover(t, sid)
	e.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/mcp-servers/%d", sid), `{"enabled":false}`, true)
	if code, _, _ := e.patchTool(qt, `{"enabled":true,"confirm_review":true}`); code != 409 {
		t.Fatalf("server off enable %d", code)
	}
	// 列表：待复核筛选
	rec := e.admin.do(http.MethodGet, "/api/v1/admin/tools?needs_review=1", "", false)
	// 切回 v1 后 create_ticket 的定义又变了，两个都待复核
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "query_ticket") || strings.Count(rec.Body.String(), `"needs_review":true`) != 2 {
		t.Fatalf("review filter %s", rec.Body)
	}
}

func TestToolACLAndUsable(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	sid, ids := setupTools(t, e)
	org := e.c.Organization.Query().FirstX(ctx)
	tx, _ := e.c.Tx(ctx)
	parent, _ := store.CreateDept(ctx, tx, org.ID, "客服部", nil)
	child, _ := store.CreateDept(ctx, tx, org.ID, "一线", &parent.ID)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	e.addUser(t, "agent@x.local", org, "", child)
	e.addUser(t, "sales@x.local", org, "", nil)
	agent := e.loginAs(t, "agent@x.local", "password123")
	sales := e.loginAs(t, "sales@x.local", "password123")
	qt, ct := ids["query_ticket"], ids["create_ticket"]

	grant := func(id int, body string, want int) {
		t.Helper()
		if rec := e.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/tools/%d/acl", id), body, true); rec.Code != want {
			t.Fatalf("grant %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	// 新工具默认禁用：有授权也不可用
	grant(qt, fmt.Sprintf(`{"subject_type":"dept","subject_id":%d,"include_children":true}`, parent.ID), 204)
	if got := myTools(t, agent); len(got) != 0 {
		t.Fatalf("disabled tool visible %v", got)
	}
	if code, _, b := e.patchTool(qt, `{"enabled":true,"exec_mode":"auto","risk_level":"read_only"}`); code != 200 {
		t.Fatalf("enable qt %s", b)
	}
	if code, _, b := e.patchTool(ct, `{"enabled":true}`); code != 200 {
		t.Fatalf("enable ct %s", b)
	}
	if got := myTools(t, agent); len(got) != 1 || got[0] != "query_ticket/auto" {
		t.Fatalf("agent tools %v", got)
	}
	if got := myTools(t, sales); len(got) != 0 {
		t.Fatalf("sales tools %v", got)
	}
	// 管理员没有授权也不可用
	if got := myTools(t, e.admin); len(got) != 0 {
		t.Fatalf("admin without grant %v", got)
	}
	// 按角色授权
	memberRole := e.c.Role.Query().Where(role.Key(rbac.Member)).OnlyX(ctx)
	grant(ct, fmt.Sprintf(`{"subject_type":"role","subject_id":%d}`, memberRole.ID), 204)
	if got := myTools(t, sales); len(got) != 1 || got[0] != "create_ticket/require_approval" {
		t.Fatalf("role grant %v", got)
	}
	// 授权对象不存在 / 类型不合法
	grant(ct, `{"subject_type":"user","subject_id":99999}`, 400)
	grant(ct, `{"subject_type":"org","subject_id":1}`, 400)
	rec := e.admin.do(http.MethodGet, fmt.Sprintf("/api/v1/admin/tools/%d/acl", qt), "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "客服部") {
		t.Fatalf("acl list %s", rec.Body)
	}
	// 撤销
	if rec := e.admin.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/tools/%d/acl/role/%d", ct, memberRole.ID), "", true); rec.Code != 204 {
		t.Fatalf("revoke %d", rec.Code)
	}
	if got := myTools(t, sales); len(got) != 0 {
		t.Fatalf("after revoke %v", got)
	}
	// 服务器停用：所有工具对员工立即不可见
	e.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/mcp-servers/%d", sid), `{"enabled":false}`, true)
	if got := myTools(t, agent); len(got) != 0 {
		t.Fatalf("server off %v", got)
	}
	e.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/mcp-servers/%d", sid), `{"enabled":true}`, true)
	// 执行策略改为禁止：不可见
	e.patchTool(qt, `{"exec_mode":"deny"}`)
	if got := myTools(t, agent); len(got) != 0 {
		t.Fatalf("deny %v", got)
	}
	if n := e.c.AuditLog.Query().Where(auditlog.ActionIn("tool.acl.grant", "tool.acl.revoke")).CountX(ctx); n != 3 {
		t.Fatalf("acl audit %d", n)
	}
}

func TestToolAccessControl(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	_, ids := setupTools(t, e)
	id := ids["create_ticket"]
	paths := []struct{ m, p string }{
		{"GET", "/api/v1/admin/tools"},
		{"GET", fmt.Sprintf("/api/v1/admin/tools/%d", id)},
		{"PATCH", fmt.Sprintf("/api/v1/admin/tools/%d", id)},
		{"GET", fmt.Sprintf("/api/v1/admin/tools/%d/acl", id)},
		{"POST", fmt.Sprintf("/api/v1/admin/tools/%d/acl", id)},
		{"DELETE", fmt.Sprintf("/api/v1/admin/tools/%d/acl/user/1", id)},
	}
	for _, p := range []string{"/api/v1/tools", paths[0].p, paths[1].p} {
		if got := get(e.h, p, nil); got != 401 {
			t.Errorf("anon %s %d", p, got)
		}
	}
	member := e.loginAs(t, "member@x.local", "password123")
	for _, r := range paths {
		if got := member.do(r.m, r.p, `{"enabled":true}`, true).Code; got != 404 {
			t.Errorf("member %s %s %d", r.m, r.p, got)
		}
	}
	other := e.c.Organization.Create().SetName("别家").SetSlug("other").SaveX(ctx)
	e.addUser(t, "oa@x.local", other, rbac.OrgAdmin, nil)
	oa := e.loginAs(t, "oa@x.local", "password123")
	for _, r := range paths[1:] {
		if got := oa.do(r.m, r.p, `{"enabled":true,"subject_type":"user","subject_id":1}`, true).Code; got != 404 {
			t.Errorf("other org %s %s %d", r.m, r.p, got)
		}
	}
	if rec := oa.do(http.MethodGet, paths[0].p, "", false); strings.Contains(rec.Body.String(), "ticket") {
		t.Fatal("other org list leaks")
	}
	if e.c.Tool.Query().Where(tool.Enabled(true)).CountX(ctx) != 0 || e.c.ACL.Query().CountX(ctx) != 0 {
		t.Fatal("越权请求不应产生副作用")
	}
}
