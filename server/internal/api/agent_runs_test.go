package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent/agentrun"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/ent/usagerecord"
)

func TestAgentRunsAndUsageAgentID(t *testing.T) {
	te := newToolEnv(t)
	ctx := context.Background()
	te.grantToolTo(t, te.query.ID, "admin@x.local")
	mid := te.c.Model.Query().Where(model.Name("ds-flash")).OnlyIDX(ctx)
	a := te.newAgent(t, fmt.Sprintf(`{"name":"用量助手","model_id":%d,"tool_ids":[%d],"max_steps":2}`, mid, te.query.ID))
	path := fmt.Sprintf("/api/v1/agents/%d", a.ID)
	if rec := te.admin.do(http.MethodPost, path+"/publish", `{}`, true); rec.Code != 200 {
		t.Fatalf("publish %d %s", rec.Code, rec.Body)
	}
	if rec := te.admin.do(http.MethodPost, path+"/acl",
		fmt.Sprintf(`{"subject_type":"user","subject_id":%s,"perm":"use"}`, te.uid(t, "member@x.local")), true); rec.Code != 204 {
		t.Fatalf("grant %d", rec.Code)
	}
	mem := te.loginAs(t, "member@x.local", "password123")
	rec := mem.do(http.MethodPost, "/api/v1/conversations", fmt.Sprintf(`{"agent_id":%d}`, a.ID), true)
	if rec.Code != 201 {
		t.Fatalf("conv %d %s", rec.Code, rec.Body)
	}
	var cv struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &cv)
	if rec := mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", cv.ID), `{"content":"你好"}`, true); rec.Code != 200 {
		t.Fatalf("send %d %s", rec.Code, rec.Body)
	}
	run := te.c.AgentRun.Query().Where(agentrun.ConversationID(cv.ID)).OnlyX(ctx)
	if rec := mem.do(http.MethodGet, fmt.Sprintf("%s/runs", path), "", false); rec.Code != 404 {
		t.Fatalf("member runs %d", rec.Code)
	}
	rec = te.admin.do(http.MethodGet, fmt.Sprintf("%s/runs", path), "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), fmt.Sprintf(`"id":%d`, run.ID)) {
		t.Fatalf("list runs %d %s", rec.Code, rec.Body)
	}
	rec = te.admin.do(http.MethodGet, fmt.Sprintf("%s/runs/%d", path, run.ID), "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"trace_id"`) {
		t.Fatalf("get run %d %s", rec.Code, rec.Body)
	}
	n := te.c.UsageRecord.Query().Where(usagerecord.AgentID(a.ID), usagerecord.PurposeEQ(usagerecord.PurposeChat)).CountX(ctx)
	if n < 1 {
		t.Fatalf("usage agent_id count %d", n)
	}
}
