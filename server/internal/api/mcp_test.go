package api_test

// M1 第五块 PR1：MCP 服务器登记（白名单校验、鉴权头加密不回显）、工具发现（默认禁用、建议风险、schema 变化待复核、下线）、
// 越权 404 / 未登录 401、审计 fail-closed（未发出任何 MCP 请求）。

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
	"github.com/SSStq1314/hengdesk/server/internal/ent/egresslog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/tool"
	"github.com/SSStq1314/hengdesk/server/internal/mcp/mockmcp"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

const mcpToken = "mcp-secret-token-9f8e7d"

type failDiscover struct{ audit.Writer }

func (w failDiscover) Write(ctx context.Context, e audit.Entry) error {
	if e.Phase == "attempt" && strings.HasSuffix(e.ResourceID, "/discover") {
		return errors.New("audit store down")
	}
	return w.Writer.Write(ctx, e)
}

type mcpEnv struct {
	*gwEnv
	mock *mockmcp.Server
}

func newMCPEnv(t *testing.T, opts ...func(*api.Deps)) *mcpEnv {
	m := mockmcp.Start(mcpToken)
	t.Cleanup(m.Close)
	return &mcpEnv{gwEnv: newGW(t, false, opts...), mock: m}
}

type mcpServerResp struct {
	ID          int
	Name        string
	HeaderNames []string `json:"header_names"`
	ToolCount   int      `json:"tool_count"`
	ReviewTools int      `json:"review_tools"`
	LastStatus  string   `json:"last_status"`
}

type mcpToolResp struct {
	Name          string
	Key           string
	Enabled       bool
	ExecMode      string `json:"exec_mode"`
	RiskLevel     string `json:"risk_level"`
	SuggestedRisk string `json:"suggested_risk"`
	NeedsReview   bool   `json:"needs_review"`
	Removed       bool
	InputSchema   map[string]any `json:"input_schema"`
}

func (e *mcpEnv) register(t *testing.T, transport, path string) mcpServerResp {
	t.Helper()
	rec := e.admin.do(http.MethodPost, "/api/v1/admin/mcp-servers", fmt.Sprintf(
		`{"name":"工单系统-%s","url":%q,"transport":%q,"headers":{"Authorization":"Bearer %s"},"timeout_sec":10}`,
		transport, e.mock.URL+path, transport, mcpToken), true)
	if rec.Code != 201 {
		t.Fatalf("register %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), mcpToken) || strings.Contains(rec.Body.String(), "headers_enc") {
		t.Fatalf("响应泄露鉴权头: %s", rec.Body)
	}
	var v mcpServerResp
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if len(v.HeaderNames) != 1 || v.HeaderNames[0] != "Authorization" {
		t.Fatalf("header names %v", v.HeaderNames)
	}
	return v
}

func (e *mcpEnv) discover(t *testing.T, id int) (int, []mcpToolResp, string) {
	t.Helper()
	rec := e.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/mcp-servers/%d/discover", id), "", true)
	var v struct{ Tools []mcpToolResp }
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return rec.Code, v.Tools, rec.Body.String()
}

func TestMCPRegisterDiscoverBothTransports(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	for _, tc := range []struct{ tr, path string }{{"streamable_http", "/mcp"}, {"sse", "/sse"}} {
		s := e.register(t, tc.tr, tc.path)
		// 数据库里是密文
		row := e.c.McpServer.GetX(ctx, s.ID)
		if row.HeadersEnc == nil || strings.Contains(string(*row.HeadersEnc), mcpToken) {
			t.Fatal("鉴权头应加密存储")
		}
		code, tools, body := e.discover(t, s.ID)
		if code != 200 || len(tools) != 2 {
			t.Fatalf("%s discover %d %s", tc.tr, code, body)
		}
		want := map[string]string{"create_ticket": "write", "query_ticket": "read_only"}
		for _, tl := range tools {
			// 新工具一律默认禁用、默认需审批；风险等级取建议值等待确认
			if tl.Enabled || tl.ExecMode != "require_approval" || tl.SuggestedRisk != want[tl.Name] || tl.RiskLevel != want[tl.Name] || tl.NeedsReview {
				t.Fatalf("%s tool %+v", tc.tr, tl)
			}
			if tl.Key != fmt.Sprintf("mcp:%d:%s", s.ID, tl.Name) || tl.InputSchema["type"] != "object" {
				t.Fatalf("key/schema %+v", tl)
			}
		}
		if strings.Contains(body, mcpToken) {
			t.Fatal("discover 响应泄露鉴权头")
		}
	}
	if e.mock.AuthFailures() != 0 {
		t.Fatalf("鉴权头未带上：%d 次 401", e.mock.AuthFailures())
	}
	// 发现不执行任何工具
	if len(e.mock.Calls("create_ticket"))+len(e.mock.Calls("query_ticket")) != 0 {
		t.Fatal("discover 不应调用工具")
	}
	// 出站日志：MCP 请求全部经统一出站客户端，模块 mcp，且不含鉴权头
	logs := e.c.EgressLog.Query().Where(egresslog.Module("mcp")).AllX(ctx)
	if len(logs) == 0 {
		t.Fatal("MCP 出站应写出站日志")
	}
	for _, l := range logs {
		if l.Decision != egresslog.DecisionAllow {
			t.Fatalf("unexpected deny %+v", l)
		}
	}
	// 审计：发现结果
	if n := e.c.AuditLog.Query().Where(auditlog.Action("mcp.server.discover"), auditlog.Outcome("ok")).CountX(ctx); n != 2 {
		t.Fatalf("discover audit %d", n)
	}
}

func TestMCPEgressWhitelistAndHeaders(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	// 不在白名单：禁止保存
	rec := e.admin.do(http.MethodPost, "/api/v1/admin/mcp-servers", `{"name":"外部","url":"http://10.9.9.9:8080/mcp"}`, true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "egress_not_allowed") || !strings.Contains(rec.Body.String(), "白名单") {
		t.Fatalf("not whitelisted %d %s", rec.Code, rec.Body)
	}
	if e.c.McpServer.Query().CountX(ctx) != 0 {
		t.Fatal("不应保存")
	}
	for _, body := range []string{
		`{"name":"x","url":"ftp://127.0.0.1/mcp"}`,
		`{"name":"x","url":"http://u:p@127.0.0.1/mcp"}`,
		`{"name":"x","url":"http://127.0.0.1/mcp","transport":"stdio"}`,
		`{"name":"x","url":"http://127.0.0.1/mcp","timeout_sec":1}`,
		`{"name":"x","url":"http://127.0.0.1/mcp","headers":{"Host":"evil"}}`,
		`{"name":"x","url":"http://127.0.0.1/mcp","headers":{"X-A":"a\r\nb"}}`,
		`{"url":"http://127.0.0.1/mcp"}`,
	} {
		if got := e.admin.do(http.MethodPost, "/api/v1/admin/mcp-servers", body, true).Code; got != 400 {
			t.Errorf("%s: %d", body, got)
		}
	}
	s := e.register(t, "streamable_http", "/mcp")
	path := fmt.Sprintf("/api/v1/admin/mcp-servers/%d", s.ID)
	// 改到白名单外：拒绝且不修改
	if rec := e.admin.do(http.MethodPatch, path, `{"url":"http://10.9.9.9/mcp"}`, true); rec.Code != 400 {
		t.Fatalf("patch url %d", rec.Code)
	}
	// 清空鉴权头后发现失败（401），错误提示不含密钥，并记失败审计
	if rec := e.admin.do(http.MethodPatch, path, `{"headers":{}}`, true); rec.Code != 200 || strings.Contains(rec.Body.String(), "Authorization") {
		t.Fatalf("clear headers %d %s", rec.Code, rec.Body)
	}
	code, _, body := e.discover(t, s.ID)
	if code != 502 || strings.Contains(body, mcpToken) {
		t.Fatalf("discover without auth %d %s", code, body)
	}
	if e.c.McpServer.GetX(ctx, s.ID).LastStatus != "failed" {
		t.Fatal("last_status")
	}
	if n := e.c.AuditLog.Query().Where(auditlog.Action("mcp.server.discover"), auditlog.Outcome("error")).CountX(ctx); n != 1 {
		t.Fatalf("fail audit %d", n)
	}
	// 重新填写后恢复
	if rec := e.admin.do(http.MethodPatch, path, fmt.Sprintf(`{"headers":{"Authorization":"Bearer %s"}}`, mcpToken), true); rec.Code != 200 {
		t.Fatalf("set headers %d", rec.Code)
	}
	if code, tools, _ := e.discover(t, s.ID); code != 200 || len(tools) != 2 {
		t.Fatalf("rediscover %d", code)
	}
	// 停用的服务器不能发现
	e.admin.do(http.MethodPatch, path, `{"enabled":false}`, true)
	if code, _, _ := e.discover(t, s.ID); code != 409 {
		t.Fatalf("disabled discover %d", code)
	}
	// 出站白名单被删后（服务器已保存），调用时仍以实时出站判定为准：拒绝并记出站阻断日志
	e.admin.do(http.MethodPatch, path, `{"enabled":true}`, true)
	e.c.EgressRule.Delete().ExecX(ctx)
	code, _, body = e.discover(t, s.ID)
	if code != 502 || !strings.Contains(body, "出站被拒绝") {
		t.Fatalf("egress removed %d %s", code, body)
	}
	if e.c.EgressLog.Query().Where(egresslog.Module("mcp"), egresslog.DecisionEQ(egresslog.DecisionDeny)).CountX(ctx) == 0 {
		t.Fatal("出站阻断应记日志")
	}
}

func TestMCPSchemaChangeAndRemoval(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	s := e.register(t, "streamable_http", "/mcp")
	if code, _, _ := e.discover(t, s.ID); code != 200 {
		t.Fatal(code)
	}
	// 管理员已启用两个工具（PR2 提供接口，这里直接改库）
	e.c.Tool.Update().Where(tool.ServerID(s.ID)).SetEnabled(true).ExecX(ctx)
	// 再次发现、无变化：保持启用
	if code, tools, _ := e.discover(t, s.ID); code != 200 || !tools[0].Enabled || !tools[1].Enabled {
		t.Fatalf("unchanged should stay enabled %+v", tools)
	}
	// 服务端：create_ticket 入参变化，query_ticket 下线
	e.mock.SetTools(true)
	code, tools, _ := e.discover(t, s.ID)
	if code != 200 || len(tools) != 2 {
		t.Fatalf("discover v2 %d", code)
	}
	by := map[string]mcpToolResp{}
	for _, tl := range tools {
		by[tl.Name] = tl
	}
	if c := by["create_ticket"]; c.Enabled || !c.NeedsReview || c.Removed {
		t.Fatalf("changed tool %+v", c)
	}
	if q := by["query_ticket"]; q.Enabled || !q.Removed {
		t.Fatalf("removed tool %+v", q)
	}
	var v mcpServerResp
	rec := e.admin.do(http.MethodGet, fmt.Sprintf("/api/v1/admin/mcp-servers/%d", s.ID), "", false)
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.ToolCount != 1 || v.ReviewTools != 1 {
		t.Fatalf("server view %+v", v)
	}
	// 恢复：下线的工具重新出现，仍禁用并待复核
	e.mock.SetTools(false)
	_, tools, _ = e.discover(t, s.ID)
	for _, tl := range tools {
		if tl.Name == "query_ticket" && (tl.Removed || tl.Enabled || !tl.NeedsReview) {
			t.Fatalf("restored %+v", tl)
		}
	}
	// 删除服务器：工具与授权一并删除
	e.c.ACL.Create().SetOrgID(1).SetResourceType(rbac.ResTool).SetResourceID(fmt.Sprintf("mcp:%d:query_ticket", s.ID)).
		SetSubjectType("user").SetSubjectID(1).SetPerms(int(rbac.BitUse)).ExecX(ctx)
	if rec := e.admin.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/mcp-servers/%d", s.ID), "", true); rec.Code != 204 {
		t.Fatalf("delete %d", rec.Code)
	}
	if e.c.Tool.Query().CountX(ctx) != 0 || e.c.ACL.Query().CountX(ctx) != 0 {
		t.Fatal("tools / acl should be deleted")
	}
}

func TestMCPAccessControl(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	s := e.register(t, "streamable_http", "/mcp")
	paths := []struct{ m, p string }{
		{"GET", "/api/v1/admin/mcp-servers"},
		{"POST", "/api/v1/admin/mcp-servers"},
		{"GET", fmt.Sprintf("/api/v1/admin/mcp-servers/%d", s.ID)},
		{"PATCH", fmt.Sprintf("/api/v1/admin/mcp-servers/%d", s.ID)},
		{"DELETE", fmt.Sprintf("/api/v1/admin/mcp-servers/%d", s.ID)},
		{"POST", fmt.Sprintf("/api/v1/admin/mcp-servers/%d/discover", s.ID)},
		{"GET", fmt.Sprintf("/api/v1/admin/mcp-servers/%d/tools", s.ID)},
	}
	for _, r := range paths {
		if got := get(e.h, r.p, nil); r.m == "GET" && got != 401 {
			t.Errorf("anon %s: %d", r.p, got)
		}
	}
	if got := (&client{t: t, h: e.h}).do(http.MethodPost, paths[1].p, `{}`, false).Code; got != 401 && got != 403 {
		t.Errorf("anon post %d", got)
	}
	org := e.c.Organization.Query().FirstX(ctx)
	e.addUser(t, "auditor@x.local", org, rbac.SecurityAuditor, nil)
	other := e.c.Organization.Create().SetName("另一家").SetSlug("other").SaveX(ctx)
	e.addUser(t, "otheradmin@x.local", other, rbac.OrgAdmin, nil)
	for _, who := range []string{"member@x.local", "auditor@x.local"} {
		cl := e.loginAs(t, who, "password123")
		for _, r := range paths {
			if got := cl.do(r.m, r.p, `{"name":"x","url":"http://127.0.0.1/mcp"}`, true).Code; got != 404 {
				t.Errorf("%s %s %s: %d", who, r.m, r.p, got)
			}
		}
	}
	// 其他组织的管理员：有 tool.manage，但看不到本组织的服务器（404），列表为空
	oa := e.loginAs(t, "otheradmin@x.local", "password123")
	for _, r := range paths[2:] {
		if got := oa.do(r.m, r.p, `{}`, true).Code; got != 404 {
			t.Errorf("other org %s %s: %d", r.m, r.p, got)
		}
	}
	rec := oa.do(http.MethodGet, paths[0].p, "", false)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "工单") {
		t.Fatalf("other org list %d %s", rec.Code, rec.Body)
	}
	if len(e.mock.Calls("query_ticket")) != 0 || e.c.McpServer.Query().CountX(ctx) != 1 {
		t.Fatal("越权请求不应产生副作用")
	}
}

func TestMCPDiscoverAuditFailClosed(t *testing.T) {
	e := newMCPEnv(t, func(d *api.Deps) { d.Audit = failDiscover{d.Audit} })
	s := e.register(t, "streamable_http", "/mcp")
	if code, _, _ := e.discover(t, s.ID); code != 503 {
		t.Fatalf("audit down: %d", code)
	}
	if e.c.EgressLog.Query().Where(egresslog.Module("mcp")).CountX(context.Background()) != 0 || e.c.Tool.Query().CountX(context.Background()) != 0 {
		t.Fatal("审计写失败时不应发出任何 MCP 请求")
	}
}
