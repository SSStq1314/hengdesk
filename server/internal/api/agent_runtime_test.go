package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent/agentrun"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
)

// TestAgentConversationRuntime：会话绑定已发布智能体 → 发消息写入 agent_runs、审计带 agent_id；
// 未授权成员 404；调试会话需 edit；无权工具静默剔除。
func TestAgentConversationRuntime(t *testing.T) {
	te := newToolEnv(t)
	ctx := context.Background()
	te.grantToolTo(t, te.query.ID, "admin@x.local")
	te.grantToolTo(t, te.create.ID, "admin@x.local")
	mid := te.c.Model.Query().Where(model.Name("ds-flash")).OnlyIDX(ctx)

	a := te.newAgent(t, fmt.Sprintf(
		`{"name":"工单助手","system_prompt":"你是工单助手，优先查工单","opening":"你好，我是工单助手","model_id":%d,"tool_ids":[%d,%d],"max_steps":2}`,
		mid, te.query.ID, te.create.ID))
	path := fmt.Sprintf("/api/v1/agents/%d", a.ID)
	if rec := te.admin.do(http.MethodPost, path+"/publish", `{"note":"v1"}`, true); rec.Code != 200 {
		t.Fatalf("publish %d %s", rec.Code, rec.Body)
	}
	if rec := te.admin.do(http.MethodPost, path+"/acl",
		fmt.Sprintf(`{"subject_type":"user","subject_id":%s,"perm":"use"}`, te.uid(t, "member@x.local")), true); rec.Code != 204 {
		t.Fatalf("grant use %d %s", rec.Code, rec.Body)
	}
	// 成员保留 query，收回 create → 运行时应静默剔除 create
	memID := te.uid(t, "member@x.local")
	if rec := te.admin.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/tools/%d/acl/user/%s", te.create.ID, memID), "", true); rec.Code != 204 {
		t.Fatalf("revoke create %d %s", rec.Code, rec.Body)
	}

	mem := te.loginAs(t, "member@x.local", "password123")
	other := te.newAgent(t, `{"name":"秘密助手"}`)
	if rec := te.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/agents/%d/publish", other.ID), `{}`, true); rec.Code != 200 {
		t.Fatalf("publish other %d", rec.Code)
	}
	if rec := mem.do(http.MethodPost, "/api/v1/conversations", fmt.Sprintf(`{"agent_id":%d}`, other.ID), true); rec.Code != 404 {
		t.Fatalf("bind unauthorized agent %d %s", rec.Code, rec.Body)
	}

	rec := mem.do(http.MethodPost, "/api/v1/conversations", fmt.Sprintf(`{"agent_id":%d}`, a.ID), true)
	if rec.Code != 201 {
		t.Fatalf("bind agent %d %s", rec.Code, rec.Body)
	}
	var cv struct {
		ID        int    `json:"id"`
		AgentID   *int   `json:"agent_id"`
		AgentName string `json:"agent_name"`
		Opening   string `json:"opening"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &cv)
	if cv.AgentID == nil || *cv.AgentID != a.ID || cv.AgentName != "工单助手" || cv.Opening == "" {
		t.Fatalf("conv view %+v body=%s", cv, rec.Body)
	}

	if rec := mem.do(http.MethodPost, "/api/v1/conversations",
		fmt.Sprintf(`{"agent_id":%d,"agent_debug":true}`, a.ID), true); rec.Code != 404 {
		t.Fatalf("member debug %d %s", rec.Code, rec.Body)
	}

	rec = mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", cv.ID),
		`{"content":"帮我查一下工单"}`, true)
	if rec.Code != 200 {
		t.Fatalf("send %d %s", rec.Code, rec.Body)
	}
	runs := te.c.AgentRun.Query().Where(agentrun.ConversationID(cv.ID)).AllX(ctx)
	if len(runs) != 1 {
		t.Fatalf("agent_runs %d", len(runs))
	}
	run := runs[0]
	if run.AgentID != a.ID || run.AgentVersion != 1 || run.Status != agentrun.StatusDone || run.MaxSteps != 2 {
		t.Fatalf("run %+v", run)
	}
	droppedCreate := false
	for _, d := range run.Dropped {
		if d.Type == "tool" && d.ID == te.create.ID {
			droppedCreate = true
		}
	}
	if !droppedCreate {
		t.Fatalf("expected create tool dropped: %+v", run.Dropped)
	}
	logs := te.c.AuditLog.Query().Where(auditlog.Action("chat.message.send"), auditlog.PhaseEQ("result")).
		Order(auditlog.ByID()).AllX(ctx)
	found := false
	for i := len(logs) - 1; i >= 0 && i >= len(logs)-8; i-- {
		if logs[i].Detail["agent_id"] == float64(a.ID) || logs[i].Detail["agent_id"] == a.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("result audit missing agent_id in last entries")
	}

	adminConv := te.admin.do(http.MethodPost, "/api/v1/conversations",
		fmt.Sprintf(`{"agent_id":%d,"agent_debug":true}`, a.ID), true)
	if adminConv.Code != 201 {
		t.Fatalf("debug conv %d %s", adminConv.Code, adminConv.Body)
	}
	var dcv struct{ ID int }
	_ = json.Unmarshal(adminConv.Body.Bytes(), &dcv)
	if rec := te.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", dcv.ID),
		`{"content":"调试一下"}`, true); rec.Code != 200 {
		t.Fatalf("debug send %d %s", rec.Code, rec.Body)
	}
	dr := te.c.AgentRun.Query().Where(agentrun.ConversationID(dcv.ID)).OnlyX(ctx)
	if !dr.Debug || dr.AgentVersion != 0 {
		t.Fatalf("debug run %+v", dr)
	}
}
