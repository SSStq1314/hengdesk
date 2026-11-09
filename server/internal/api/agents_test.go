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
	"github.com/SSStq1314/hengdesk/server/internal/ent/agent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

type agentResp struct {
	ID               int
	Name             string
	Opening          string
	Published        bool
	PublishedVersion int  `json:"published_version"`
	DraftDirty       bool `json:"draft_dirty"`
	Perms            []string
	Draft            *struct {
		SystemPrompt string `json:"system_prompt"`
		ToolIDs      []int  `json:"tool_ids"`
		MaxSteps     int    `json:"max_steps"`
	}
}

func decodeAgent(t *testing.T, body []byte) agentResp {
	t.Helper()
	var a agentResp
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

// grantToolTo 给用户授予工具 use（管理员也必须有授权才可绑定）。
func (te *toolEnv) grantToolTo(t *testing.T, toolID int, email string) {
	t.Helper()
	if rec := te.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/tools/%d/acl", toolID),
		fmt.Sprintf(`{"subject_type":"user","subject_id":%s}`, te.uid(t, email)), true); rec.Code != 204 {
		t.Fatalf("grant tool %d %s", rec.Code, rec.Body)
	}
}

func (te *toolEnv) newAgent(t *testing.T, body string) agentResp {
	t.Helper()
	rec := te.admin.do(http.MethodPost, "/api/v1/agents", body, true)
	if rec.Code != 201 {
		t.Fatalf("create agent %d %s", rec.Code, rec.Body)
	}
	return decodeAgent(t, rec.Body.Bytes())
}

func TestAgentCRUDPublishVisibility(t *testing.T) {
	te := newToolEnv(t)
	ctx := context.Background()
	te.grantToolTo(t, te.query.ID, "admin@x.local")
	mid := te.c.Model.Query().Where(model.Name("ds-flash")).OnlyIDX(ctx)

	// 普通成员没有 agent.create：404
	if rec := te.mem.do(http.MethodPost, "/api/v1/agents", `{"name":"x"}`, true); rec.Code != 404 {
		t.Fatalf("member create %d", rec.Code)
	}
	// 校验：步数越界 400；绑定本人不可用的工具 404
	if rec := te.admin.do(http.MethodPost, "/api/v1/agents", `{"name":"x","max_steps":11}`, true); rec.Code != 400 {
		t.Fatalf("max_steps %d", rec.Code)
	}
	if rec := te.admin.do(http.MethodPost, "/api/v1/agents", fmt.Sprintf(`{"name":"x","tool_ids":[%d]}`, te.create.ID), true); rec.Code != 404 {
		t.Fatalf("unusable tool %d", rec.Code)
	}
	a := te.newAgent(t, fmt.Sprintf(`{"name":"工单助手","system_prompt":"你是工单助手","opening":"你好，我能查工单","model_id":%d,"tool_ids":[%d],"max_steps":3}`, mid, te.query.ID))
	if a.Published || a.Draft == nil || a.Draft.MaxSteps != 3 || len(a.Draft.ToolIDs) != 1 {
		t.Fatalf("created %+v", a)
	}
	path := fmt.Sprintf("/api/v1/agents/%d", a.ID)
	// 授权成员 use；未发布时成员仍不可见（列表无、详情 404）
	if rec := te.admin.do(http.MethodPost, path+"/acl", fmt.Sprintf(`{"subject_type":"user","subject_id":%s,"perm":"use"}`, te.uid(t, "member@x.local")), true); rec.Code != 204 {
		t.Fatalf("grant %d %s", rec.Code, rec.Body)
	}
	if rec := te.mem.do(http.MethodGet, path, "", false); rec.Code != 404 {
		t.Fatalf("unpublished visible to member %d", rec.Code)
	}
	if rec := te.mem.do(http.MethodGet, "/api/v1/agents", "", false); strings.Contains(rec.Body.String(), "工单助手") {
		t.Fatalf("unpublished in square %s", rec.Body)
	}
	// 发布后成员可见，但看不到草稿配置（系统提示词）
	if rec := te.admin.do(http.MethodPost, path+"/publish", `{"note":"v1"}`, true); rec.Code != 200 {
		t.Fatalf("publish %d %s", rec.Code, rec.Body)
	}
	rec := te.mem.do(http.MethodGet, path, "", false)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "你是工单助手") {
		t.Fatalf("member get %d %s", rec.Code, rec.Body)
	}
	if rec := te.mem.do(http.MethodGet, "/api/v1/agents", "", false); !strings.Contains(rec.Body.String(), "工单助手") {
		t.Fatalf("square %s", rec.Body)
	}
	// 成员无 edit / manage：改、发布、删、授权、版本一律 404
	for _, r := range []struct{ m, p, b string }{{http.MethodPatch, path, `{"name":"y"}`}, {http.MethodPost, path + "/publish", `{}`},
		{http.MethodDelete, path, ""}, {http.MethodGet, path + "/acl", ""}, {http.MethodGet, path + "/versions", ""}} {
		if rec := te.mem.do(r.m, r.p, r.b, true); rec.Code != 404 {
			t.Fatalf("%s %s member %d", r.m, r.p, rec.Code)
		}
	}
	// 草稿修改不影响线上版本
	if rec := te.admin.do(http.MethodPatch, path, `{"name":"工单助手2","opening":"新开场"}`, true); rec.Code != 200 {
		t.Fatalf("patch %d %s", rec.Code, rec.Body)
	}
	mv := decodeAgent(t, te.mem.do(http.MethodGet, path, "", false).Body.Bytes())
	if mv.Name != "工单助手" || mv.Opening != "你好，我能查工单" || mv.Draft != nil {
		t.Fatalf("member sees draft %+v", mv)
	}
	av := decodeAgent(t, te.admin.do(http.MethodGet, path, "", false).Body.Bytes())
	if !av.DraftDirty || av.Name != "工单助手2" {
		t.Fatalf("admin view %+v", av)
	}
	// 再次发布 → v2；恢复 v1 为草稿
	_ = te.admin.do(http.MethodPost, path+"/publish", `{}`, true)
	if rec := te.admin.do(http.MethodPost, path+"/versions/1/restore", "", true); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"工单助手"`) {
		t.Fatalf("restore %d %s", rec.Code, rec.Body)
	}
	if rec := te.admin.do(http.MethodPost, path+"/versions/9/restore", "", true); rec.Code != 404 {
		t.Fatalf("restore missing %d", rec.Code)
	}
	vs := te.admin.do(http.MethodGet, path+"/versions", "", false).Body.String()
	if !strings.Contains(vs, `"version":2`) || !strings.Contains(vs, `"version":1`) {
		t.Fatalf("versions %s", vs)
	}
	// 不能撤销最后一个 manage
	if rec := te.admin.do(http.MethodDelete, fmt.Sprintf("%s/acl/user/%s", path, te.uid(t, "admin@x.local")), "", true); rec.Code != 409 {
		t.Fatalf("revoke last manage %d", rec.Code)
	}
	// 未登录 401
	if code := get(te.h, path, nil); code != 401 {
		t.Fatalf("anon %d", code)
	}
	// 软删除后一律 404
	if rec := te.admin.do(http.MethodDelete, path, "", true); rec.Code != 204 {
		t.Fatalf("delete %d", rec.Code)
	}
	if rec := te.admin.do(http.MethodGet, path, "", false); rec.Code != 404 {
		t.Fatalf("deleted get %d", rec.Code)
	}
	for _, act := range []string{"agent.create", "agent.publish", "agent.update", "agent.restore", "agent.acl.grant", "agent.delete"} {
		if !te.c.AuditLog.Query().Where(auditlog.Action(act), auditlog.PhaseEQ(auditlog.PhaseAttempt)).ExistX(ctx) {
			t.Fatalf("missing audit %s", act)
		}
	}
}

// AC-03：部门管理员创建的智能体默认共享给本部门（发布后可见），对其他部门不可见（列表无、详情 404），且不能共享到子树外。
func TestAgentDeptScope(t *testing.T) {
	te := newToolEnv(t)
	ctx := context.Background()
	org := te.c.Organization.Query().FirstX(ctx)
	tx, _ := te.c.Tx(ctx)
	fin, _ := store.CreateDept(ctx, tx, org.ID, "财务部", nil)
	mkt, _ := store.CreateDept(ctx, tx, org.ID, "市场部", nil)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	te.addUser(t, "finadmin@x.local", org, rbac.DeptAdmin, fin)
	te.addUser(t, "fin@x.local", org, "", fin)
	te.addUser(t, "mkt@x.local", org, "", mkt)
	da := te.loginAs(t, "finadmin@x.local", "password123")
	rec := da.do(http.MethodPost, "/api/v1/agents", `{"name":"报销助手"}`, true)
	if rec.Code != 201 {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	a := decodeAgent(t, rec.Body.Bytes())
	path := fmt.Sprintf("/api/v1/agents/%d", a.ID)
	if rec := da.do(http.MethodPost, path+"/acl", fmt.Sprintf(`{"subject_type":"dept","subject_id":%d,"perm":"use"}`, mkt.ID), true); rec.Code != 404 {
		t.Fatalf("share outside subtree %d", rec.Code)
	}
	fc, mc := te.loginAs(t, "fin@x.local", "password123"), te.loginAs(t, "mkt@x.local", "password123")
	if rec := fc.do(http.MethodGet, path, "", false); rec.Code != 404 {
		t.Fatalf("unpublished visible %d", rec.Code)
	}
	if rec := da.do(http.MethodPost, path+"/publish", `{}`, true); rec.Code != 200 {
		t.Fatalf("publish %d", rec.Code)
	}
	if rec := fc.do(http.MethodGet, path, "", false); rec.Code != 200 {
		t.Fatalf("fin get %d", rec.Code)
	}
	if rec := mc.do(http.MethodGet, path, "", false); rec.Code != 404 {
		t.Fatalf("mkt get %d", rec.Code)
	}
	if rec := mc.do(http.MethodGet, "/api/v1/agents", "", false); strings.Contains(rec.Body.String(), "报销助手") {
		t.Fatalf("mkt list %s", rec.Body)
	}
}

type failAgentAudit struct{ audit.Writer }

func (w failAgentAudit) Write(ctx context.Context, e audit.Entry) error {
	if strings.HasPrefix(e.Action, "agent.") {
		return errors.New("audit store down")
	}
	return w.Writer.Write(ctx, e)
}

// 审计写失败拒绝执行：不落库。
func TestAgentAuditFailClosed(t *testing.T) {
	te := newToolEnv(t, func(d *api.Deps) { d.Audit = failAgentAudit{d.Audit} })
	if rec := te.admin.do(http.MethodPost, "/api/v1/agents", `{"name":"x"}`, true); rec.Code < 500 {
		t.Fatalf("expected 5xx, got %d", rec.Code)
	}
	if n := te.c.Agent.Query().Where(agent.Name("x")).CountX(context.Background()); n != 0 {
		t.Fatalf("agent created despite audit failure")
	}
}
