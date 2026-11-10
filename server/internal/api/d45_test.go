package api_test

// D-45：智能体运行时默认值。第 4 条越权（工具/知识库/模型/审批发起人）、第 2 条回滚、
// 第 5 条高风险审批、第 8 条停用与只读。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent/agentrun"
	entapproval "github.com/SSStq1314/hengdesk/server/internal/ent/approval"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/ent/tool"
)

// TestD45RuntimeIntersection 第 4 条 a–d：调用人权限 ∩ 智能体绑定；无权工具对模型不可见；
// 知识库按调用人 ACL 剔除；审批单发起人记为调用人。
func TestD45RuntimeIntersection(t *testing.T) {
	te := newToolEnv(t)
	ctx := context.Background()
	te.grantToolTo(t, te.query.ID, "admin@x.local")
	te.grantToolTo(t, te.create.ID, "admin@x.local")
	mid := te.c.Model.Query().Where(model.Name("ds-flash")).OnlyIDX(ctx)

	// 建知识库：只给管理员 view，不给成员 → 运行时应剔除
	pid := te.c.ModelProvider.Query().OnlyX(ctx).ID
	if rec := te.admin.do(http.MethodPost, "/api/v1/admin/models",
		fmt.Sprintf(`{"name":"emb","provider_id":%d,"upstream_model":"mock-embed"}`, pid), true); rec.Code != 201 {
		t.Fatalf("create emb %d %s", rec.Code, rec.Body)
	}
	rec := te.admin.do(http.MethodPost, "/api/v1/kbs", `{"name":"财务制度","embedding_model":"emb"}`, true)
	if rec.Code != 201 {
		t.Fatalf("create kb %d %s", rec.Code, rec.Body)
	}
	var kb struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &kb)
	if kb.ID == 0 {
		t.Fatal("kb id")
	}

	a := te.newAgent(t, fmt.Sprintf(
		`{"name":"交集助手","system_prompt":"助手","opening":"你好","model_id":%d,"kb_ids":[%d],"tool_ids":[%d,%d],"max_steps":5}`,
		mid, kb.ID, te.query.ID, te.create.ID))
	path := fmt.Sprintf("/api/v1/agents/%d", a.ID)
	if rec := te.admin.do(http.MethodPost, path+"/publish", `{"note":"v1"}`, true); rec.Code != 200 {
		t.Fatalf("publish %d %s", rec.Code, rec.Body)
	}
	memID := te.uid(t, "member@x.local")
	if rec := te.admin.do(http.MethodPost, path+"/acl",
		fmt.Sprintf(`{"subject_type":"user","subject_id":%s,"perm":"use"}`, memID), true); rec.Code != 204 {
		t.Fatalf("grant use %d %s", rec.Code, rec.Body)
	}
	// 成员保留 query，收回 create
	if rec := te.admin.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/tools/%d/acl/user/%s", te.create.ID, memID), "", true); rec.Code != 204 {
		t.Fatalf("revoke create %d %s", rec.Code, rec.Body)
	}

	mem := te.loginAs(t, "member@x.local", "password123")
	rec = mem.do(http.MethodPost, "/api/v1/conversations", fmt.Sprintf(`{"agent_id":%d}`, a.ID), true)
	if rec.Code != 201 {
		t.Fatalf("bind %d %s", rec.Code, rec.Body)
	}
	var cv struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &cv)

	// 触发工具调用（create 需审批）；同时验证 tools 列表不含 create
	te.m.ToolArgs = `{"title":"越权测试"}`
	nBefore := len(te.m.Requests)
	rec = mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", cv.ID),
		`{"content":"请创建工单"}`, true)
	if rec.Code != 200 {
		t.Fatalf("send %d %s", rec.Code, rec.Body)
	}
	if len(te.m.Requests) <= nBefore {
		t.Fatal("expected model request")
	}
	req := te.m.Requests[len(te.m.Requests)-1]
	toolsRaw, _ := json.Marshal(req["tools"])
	toolsStr := string(toolsRaw)
	if strings.Contains(toolsStr, "create_ticket") {
		t.Fatalf("无权工具不应出现在模型 tools 列表: %s", toolsStr)
	}
	if !strings.Contains(toolsStr, "query_ticket") {
		t.Fatalf("有权工具应对模型可见: %s", toolsStr)
	}

	run := te.c.AgentRun.Query().Where(agentrun.ConversationID(cv.ID)).OnlyX(ctx)
	droppedKB, droppedCreate := false, false
	for _, d := range run.Dropped {
		if d.Type == "kb" && d.ID == kb.ID {
			droppedKB = true
		}
		if d.Type == "tool" && d.ID == te.create.ID {
			droppedCreate = true
		}
	}
	if !droppedKB {
		t.Fatalf("expected kb dropped: %+v", run.Dropped)
	}
	if !droppedCreate {
		t.Fatalf("expected create tool dropped: %+v", run.Dropped)
	}

	// 另建只绑 create 的智能体：审批单发起人必须是调用人（成员），不是智能体创建者。
	te.grantToolTo(t, te.create.ID, "member@x.local")
	a2 := te.newAgent(t, fmt.Sprintf(`{"name":"审批助手","model_id":%d,"tool_ids":[%d],"max_steps":3}`, mid, te.create.ID))
	path2 := fmt.Sprintf("/api/v1/agents/%d", a2.ID)
	if rec := te.admin.do(http.MethodPost, path2+"/publish", `{}`, true); rec.Code != 200 {
		t.Fatalf("publish a2 %d", rec.Code)
	}
	if rec := te.admin.do(http.MethodPost, path2+"/acl",
		fmt.Sprintf(`{"subject_type":"user","subject_id":%s,"perm":"use"}`, memID), true); rec.Code != 204 {
		t.Fatalf("grant a2 %d", rec.Code)
	}
	rec = mem.do(http.MethodPost, "/api/v1/conversations", fmt.Sprintf(`{"agent_id":%d}`, a2.ID), true)
	if rec.Code != 201 {
		t.Fatalf("bind a2 %d %s", rec.Code, rec.Body)
	}
	var cv2 struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &cv2)
	te.m.ToolArgs = `{"title":"需要审批"}`
	rec = mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", cv2.ID),
		`{"content":"创建一个工单"}`, true)
	if rec.Code != 200 {
		t.Fatalf("send2 %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "event: approval_pending") {
		t.Fatalf("expected approval: %s", rec.Body)
	}
	ap := te.c.Approval.Query().Where(entapproval.ConversationID(cv2.ID)).OnlyX(ctx)
	if ap.RequesterID != atoi(memID) {
		t.Fatalf("requester want %s got %d (admin=%s)", memID, ap.RequesterID, te.uid(t, "admin@x.local"))
	}
}

// TestD45HighRiskAlwaysApproval 第 5 条：智能体绑定的高风险工具一律走审批（即使 exec_mode=auto）。
func TestD45HighRiskAlwaysApproval(t *testing.T) {
	te := newToolEnv(t)
	ctx := context.Background()
	te.grantToolTo(t, te.create.ID, "admin@x.local")
	te.c.Tool.UpdateOneID(te.create.ID).SetRiskLevel(tool.RiskLevelHighRisk).SetExecMode(tool.ExecModeAuto).ExecX(ctx)
	mid := te.c.Model.Query().Where(model.Name("ds-flash")).OnlyIDX(ctx)
	a := te.newAgent(t, fmt.Sprintf(`{"name":"高危助手","model_id":%d,"tool_ids":[%d],"max_steps":3}`, mid, te.create.ID))
	path := fmt.Sprintf("/api/v1/agents/%d", a.ID)
	if rec := te.admin.do(http.MethodPost, path+"/publish", `{}`, true); rec.Code != 200 {
		t.Fatalf("publish %d", rec.Code)
	}
	memID := te.uid(t, "member@x.local")
	if rec := te.admin.do(http.MethodPost, path+"/acl",
		fmt.Sprintf(`{"subject_type":"user","subject_id":%s,"perm":"use"}`, memID), true); rec.Code != 204 {
		t.Fatalf("grant %d", rec.Code)
	}
	te.grantToolTo(t, te.create.ID, "member@x.local")
	mem := te.loginAs(t, "member@x.local", "password123")
	rec := mem.do(http.MethodPost, "/api/v1/conversations", fmt.Sprintf(`{"agent_id":%d}`, a.ID), true)
	if rec.Code != 201 {
		t.Fatalf("bind %d %s", rec.Code, rec.Body)
	}
	var cv struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &cv)
	te.m.ToolArgs = `{"title":"高危"}`
	rec = mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", cv.ID), `{"content":"执行"}`, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "event: approval_pending") {
		t.Fatalf("high risk must approve: %d %s", rec.Code, rec.Body)
	}
	if n := len(te.mock.Calls("create_ticket")); n != 0 {
		t.Fatalf("must not auto-exec high risk: %d", n)
	}
}

// TestD45PublishRollbackDisable 第 2 / 8 条：发布快照+审计、回滚、停用与只读、广场隐藏。
func TestD45PublishRollbackDisable(t *testing.T) {
	te := newToolEnv(t)
	ctx := context.Background()
	te.grantToolTo(t, te.query.ID, "admin@x.local")
	mid := te.c.Model.Query().Where(model.Name("ds-flash")).OnlyIDX(ctx)
	a := te.newAgent(t, fmt.Sprintf(`{"name":"v1助手","opening":"开场1","model_id":%d,"tool_ids":[%d],"max_steps":5}`, mid, te.query.ID))
	path := fmt.Sprintf("/api/v1/agents/%d", a.ID)
	if rec := te.admin.do(http.MethodPost, path+"/publish", `{"note":"第一版"}`, true); rec.Code != 200 {
		t.Fatalf("publish1 %d %s", rec.Code, rec.Body)
	}
	if !te.c.AuditLog.Query().Where(auditlog.Action("agent.publish"), auditlog.PhaseEQ(auditlog.PhaseAttempt)).ExistX(ctx) {
		t.Fatal("missing publish audit")
	}
	if rec := te.admin.do(http.MethodPatch, path, `{"name":"v2助手","opening":"开场2"}`, true); rec.Code != 200 {
		t.Fatalf("patch %d", rec.Code)
	}
	if rec := te.admin.do(http.MethodPost, path+"/publish", `{"note":"第二版"}`, true); rec.Code != 200 {
		t.Fatalf("publish2 %d", rec.Code)
	}
	memID := te.uid(t, "member@x.local")
	if rec := te.admin.do(http.MethodPost, path+"/acl",
		fmt.Sprintf(`{"subject_type":"user","subject_id":%s,"perm":"use"}`, memID), true); rec.Code != 204 {
		t.Fatalf("grant %d", rec.Code)
	}
	mem := te.loginAs(t, "member@x.local", "password123")
	// 成员看到 v2
	got := decodeAgent(t, mem.do(http.MethodGet, path, "", false).Body.Bytes())
	if got.Name != "v2助手" || got.Opening != "开场2" || got.PublishedVersion != 2 {
		t.Fatalf("member sees %+v", got)
	}
	// 回滚到 v1（manage）
	if rec := te.mem.do(http.MethodPost, path+"/versions/1/rollback", "", true); rec.Code != 404 {
		t.Fatalf("member rollback %d", rec.Code)
	}
	rec := te.admin.do(http.MethodPost, path+"/versions/1/rollback", "", true)
	if rec.Code != 200 {
		t.Fatalf("rollback %d %s", rec.Code, rec.Body)
	}
	got = decodeAgent(t, rec.Body.Bytes())
	if got.PublishedVersion != 1 || got.Name != "v1助手" {
		// admin with edit sees draft; published version should be 1
		if got.PublishedVersion != 1 {
			t.Fatalf("rollback version %+v", got)
		}
	}
	if !te.c.AuditLog.Query().Where(auditlog.Action("agent.rollback"), auditlog.PhaseEQ(auditlog.PhaseAttempt)).ExistX(ctx) {
		t.Fatal("missing rollback audit")
	}
	mv := decodeAgent(t, mem.do(http.MethodGet, path, "", false).Body.Bytes())
	if mv.Name != "v1助手" || mv.Opening != "开场1" || mv.PublishedVersion != 1 {
		t.Fatalf("member after rollback %+v", mv)
	}

	// 开会话后停用 → 只读；广场不可见；新开会话 404
	rec = mem.do(http.MethodPost, "/api/v1/conversations", fmt.Sprintf(`{"agent_id":%d}`, a.ID), true)
	if rec.Code != 201 {
		t.Fatalf("bind %d %s", rec.Code, rec.Body)
	}
	var cv struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &cv)
	if rec := te.mem.do(http.MethodPost, path+"/disable", "", true); rec.Code != 404 {
		t.Fatalf("member disable %d", rec.Code)
	}
	if rec := te.admin.do(http.MethodPost, path+"/disable", "", true); rec.Code != 200 {
		t.Fatalf("disable %d %s", rec.Code, rec.Body)
	}
	if !te.c.AuditLog.Query().Where(auditlog.Action("agent.disable"), auditlog.PhaseEQ(auditlog.PhaseAttempt)).ExistX(ctx) {
		t.Fatal("missing disable audit")
	}
	if rec := mem.do(http.MethodGet, "/api/v1/agents", "", false); strings.Contains(rec.Body.String(), "v1助手") || strings.Contains(rec.Body.String(), "v2助手") {
		t.Fatalf("plaza still shows disabled: %s", rec.Body)
	}
	if rec := mem.do(http.MethodGet, path, "", false); rec.Code != 404 {
		t.Fatalf("disabled detail for use-only %d", rec.Code)
	}
	if rec := mem.do(http.MethodPost, "/api/v1/conversations", fmt.Sprintf(`{"agent_id":%d}`, a.ID), true); rec.Code != 404 {
		t.Fatalf("new bind disabled %d", rec.Code)
	}
	grec := mem.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", cv.ID), "", false)
	if grec.Code != 200 {
		t.Fatalf("get conv %d %s", grec.Code, grec.Body)
	}
	var wrap struct {
		Conversation struct {
			AgentReadonly bool `json:"agent_readonly"`
		} `json:"conversation"`
	}
	_ = json.Unmarshal(grec.Body.Bytes(), &wrap)
	if !wrap.Conversation.AgentReadonly {
		t.Fatalf("expected agent_readonly: %s", grec.Body)
	}
	if rec := mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", cv.ID),
		`{"content":"还能说吗"}`, true); rec.Code != 409 {
		t.Fatalf("readonly send want 409 got %d %s", rec.Code, rec.Body)
	}
	// 启用恢复
	if rec := te.admin.do(http.MethodPost, path+"/enable", "", true); rec.Code != 200 {
		t.Fatalf("enable %d", rec.Code)
	}
	if !te.c.AuditLog.Query().Where(auditlog.Action("agent.enable"), auditlog.PhaseEQ(auditlog.PhaseAttempt)).ExistX(ctx) {
		t.Fatal("missing enable audit")
	}
	if rec := mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", cv.ID),
		`{"content":"又能说了"}`, true); rec.Code != 200 {
		t.Fatalf("after enable %d %s", rec.Code, rec.Body)
	}

	// 软删除后已有会话只读
	if rec := te.admin.do(http.MethodDelete, path, "", true); rec.Code != 204 {
		t.Fatalf("delete %d", rec.Code)
	}
	if rec := mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", cv.ID),
		`{"content":"删除后"}`, true); rec.Code != 409 {
		t.Fatalf("deleted readonly want 409 got %d %s", rec.Code, rec.Body)
	}
}

// TestD45DefaultMaxSteps 第 5 条：步数默认 5，范围 1–10。
func TestD45DefaultMaxSteps(t *testing.T) {
	te := newToolEnv(t)
	a := te.newAgent(t, `{"name":"默认步数"}`)
	if a.Draft == nil || a.Draft.MaxSteps != 5 {
		t.Fatalf("default max_steps %+v", a.Draft)
	}
	if rec := te.admin.do(http.MethodPost, "/api/v1/agents", `{"name":"坏","max_steps":11}`, true); rec.Code != 400 {
		t.Fatalf("max_steps 11 want 400 got %d", rec.Code)
	}
	b := te.newAgent(t, `{"name":"步数0","max_steps":0}`)
	if b.Draft == nil || b.Draft.MaxSteps != 5 {
		t.Fatalf("max_steps 0 → default 5: %+v", b.Draft)
	}
}
