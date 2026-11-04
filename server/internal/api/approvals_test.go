package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/tool"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
)

type approvalResp struct {
	ID          int
	Status      string
	ExecStatus  string `json:"exec_status"`
	Result      string
	Args        map[string]any
	CanDecide   bool `json:"can_decide"`
	CanWithdraw bool `json:"can_withdraw"`
}

func approvalList(t *testing.T, cl *client, q string) []approvalResp {
	t.Helper()
	rec := cl.do(http.MethodGet, "/api/v1/approvals?"+q, "", false)
	if rec.Code != 200 {
		t.Fatalf("list %d %s", rec.Code, rec.Body)
	}
	var v struct{ Items []approvalResp }
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return v.Items
}

func TestApprovalCenterEndToEnd(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	srv := e.register(t, "streamable_http", "/mcp")
	if code, _, body := e.discover(t, srv.ID); code != 200 {
		t.Fatalf("discover %d %s", code, body)
	}
	org := e.c.Organization.Query().FirstX(ctx)
	appr := e.addUser(t, "appr@x.local", org, "", nil)
	e.addUser(t, "nosy@x.local", org, "", nil)
	member := e.c.User.Query().Where(user.Email("member@x.local")).OnlyX(ctx)
	tl := e.c.Tool.Query().Where(tool.Name("create_ticket")).OnlyX(ctx)
	tl = e.c.Tool.UpdateOne(tl).SetEnabled(true).SetNeedsReview(false).SetRiskLevel("write").SetApproverIds([]int{appr.ID}).SaveX(ctx)
	mk := func() *ent.Approval {
		return e.c.Approval.Create().SetOrgID(org.ID).SetToolID(tl.ID).SetToolKey(tl.Key).SetToolName(tl.Name).
			SetRequesterID(member.ID).SetArgs(map[string]any{"title": "打印机坏了"}).SetApproverIds([]int{appr.ID}).
			SetExpiresAt(time.Now().Add(time.Hour)).SaveX(ctx)
	}
	ap := mk()
	path := fmt.Sprintf("/api/v1/approvals/%d", ap.ID)

	if code := get(e.h, "/api/v1/approvals", nil); code != http.StatusUnauthorized {
		t.Fatalf("anon %d", code)
	}
	nosy, mem, ac := e.loginAs(t, "nosy@x.local", "password123"), e.loginAs(t, "member@x.local", "password123"), e.loginAs(t, "appr@x.local", "password123")
	if rec := nosy.do(http.MethodGet, path, "", false); rec.Code != 404 {
		t.Fatalf("nosy get %d", rec.Code)
	}
	if rec := nosy.do(http.MethodPost, path+"/approve", `{}`, true); rec.Code != 404 {
		t.Fatalf("nosy approve %d", rec.Code)
	}
	if rec := e.admin.do(http.MethodPost, path+"/approve", `{}`, true); rec.Code != 404 {
		t.Fatalf("指定审批人后管理员不能审批 %d", rec.Code)
	}
	if rec := mem.do(http.MethodPost, path+"/approve", `{}`, true); rec.Code != 403 {
		t.Fatalf("self approve %d", rec.Code)
	}
	if len(approvalList(t, nosy, "box=todo")) != 0 {
		t.Fatal("nosy todo")
	}
	todo := approvalList(t, ac, "box=todo")
	if len(todo) != 1 || !todo[0].CanDecide || todo[0].Args["title"] != "打印机坏了" {
		t.Fatalf("todo %+v", todo)
	}
	if mine := approvalList(t, mem, "box=mine"); len(mine) != 1 || !mine[0].CanWithdraw || mine[0].CanDecide {
		t.Fatalf("mine %+v", mine)
	}
	if n := len(e.mock.Calls("create_ticket")); n != 0 {
		t.Fatalf("审批前 MCP 服务端收到 %d 次调用", n)
	}
	rec := ac.do(http.MethodPost, path+"/approve", `{"comment":"同意"}`, true)
	if rec.Code != 200 {
		t.Fatalf("approve %d %s", rec.Code, rec.Body)
	}
	var v approvalResp
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Status != "approved" || v.ExecStatus != "succeeded" || !strings.Contains(v.Result, "T-") {
		t.Fatalf("approved %+v", v)
	}
	calls := e.mock.Calls("create_ticket")
	if len(calls) != 1 || calls[0]["title"] != "打印机坏了" {
		t.Fatalf("calls %v", calls)
	}
	if rec := ac.do(http.MethodPost, path+"/approve", `{}`, true); rec.Code != 409 {
		t.Fatalf("again %d", rec.Code)
	}
	if len(e.mock.Calls("create_ticket")) != 1 {
		t.Fatal("重复执行")
	}
	if !e.c.AuditLog.Query().Where(auditlog.Action("tool.call"), auditlog.ActorID(member.ID)).ExistX(ctx) {
		t.Fatal("tool.call audit")
	}

	// 拒绝与撤回
	a2, a3 := mk(), mk()
	if rec := ac.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/reject", a2.ID), `{"comment":"不行"}`, true); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `"status":"rejected"`) {
		t.Fatalf("reject %d %s", rec.Code, rec.Body)
	}
	if rec := ac.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/withdraw", a3.ID), ``, true); rec.Code != 404 {
		t.Fatalf("approver withdraw %d", rec.Code)
	}
	if rec := mem.do(http.MethodPost, fmt.Sprintf("/api/v1/approvals/%d/withdraw", a3.ID), ``, true); rec.Code != 200 {
		t.Fatalf("withdraw %d %s", rec.Code, rec.Body)
	}
	if len(e.mock.Calls("create_ticket")) != 1 {
		t.Fatal("拒绝 / 撤回不得执行")
	}
	if done := approvalList(t, ac, "box=todo&status=done"); len(done) != 3 {
		t.Fatalf("done %d", len(done))
	}
}
