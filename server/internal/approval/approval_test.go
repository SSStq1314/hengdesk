package approval_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/approval"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	entapproval "github.com/SSStq1314/hengdesk/server/internal/ent/approval"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/notification"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

type fakeCaller struct {
	n    atomic.Int32
	args []map[string]any
	mu   sync.Mutex
}

func (f *fakeCaller) Call(_ context.Context, t *ent.Tool, _ *ent.McpServer, args map[string]any) (string, bool, error) {
	f.n.Add(1)
	f.mu.Lock()
	f.args = append(f.args, args)
	f.mu.Unlock()
	return "ok:" + t.Name, false, nil
}

type flakyWriter struct {
	audit.Writer
	fail atomic.Bool
}

func (w *flakyWriter) Write(ctx context.Context, e audit.Entry) error {
	if w.fail.Load() && e.Phase == "attempt" {
		return errors.New("audit store down")
	}
	return w.Writer.Write(ctx, e)
}

type fixture struct {
	c      *ent.Client
	svc    *approval.Service
	caller *fakeCaller
	w      *flakyWriter
	tool   *ent.Tool
	req    approval.Actor
	appr   approval.Actor
	other  approval.Actor
	admin  approval.Actor
	now    time.Time
	orgID  int
}

func setup(t *testing.T, approvers bool) *fixture {
	t.Helper()
	ctx := context.Background()
	c := storetest.NewTestClient(t)
	org := c.Organization.Create().SetName("o").SetSlug("o").SaveX(ctx)
	mk := func(email string) *ent.User { return c.User.Create().SetEmail(email).SetOrgID(org.ID).SaveX(ctx) }
	u1, u2, u3, u4 := mk("req@x"), mk("appr@x"), mk("other@x"), mk("admin@x")
	srv := c.McpServer.Create().SetOrgID(org.ID).SetName("s").SetURL("http://127.0.0.1:1/mcp").SetCreatedBy(u4.ID).SaveX(ctx)
	tc := c.Tool.Create().SetKey("mcp:1:create_ticket").SetName("create_ticket").SetOrgID(org.ID).SetServerID(srv.ID).
		SetEnabled(true).SetRiskLevel("write").SetApprovalTimeoutSec(600)
	if approvers {
		tc.SetApproverIds([]int{u2.ID, u1.ID})
	}
	f := &fixture{c: c, caller: &fakeCaller{}, w: &flakyWriter{Writer: audit.EntWriter{C: c}}, tool: tc.SaveX(ctx),
		req: approval.Actor{OrgID: org.ID, ID: u1.ID, Email: "req@x"}, appr: approval.Actor{OrgID: org.ID, ID: u2.ID, Email: "appr@x"},
		other: approval.Actor{OrgID: org.ID, ID: u3.ID, Email: "other@x"}, admin: approval.Actor{OrgID: org.ID, ID: u4.ID, Email: "admin@x", CanManage: true},
		now: time.Now(), orgID: org.ID}
	f.svc = &approval.Service{DB: c, Audit: f.w, Caller: f.caller, Now: func() time.Time { return f.now }}
	return f
}

func (f *fixture) create(t *testing.T) *ent.Approval {
	t.Helper()
	ap, err := f.svc.Create(context.Background(), f.req, approval.CreateInput{Tool: f.tool,
		Args: map[string]any{"title": "打印机坏了", "api_token": "s3cr3t"}, ContextSummary: "用户：帮我建个工单"})
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

func TestApproveExecutesExactlyOnceWithSnapshot(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	ap := f.create(t)
	if f.caller.n.Load() != 0 {
		t.Fatal("审批前不得调用")
	}
	// 发起人在审批人名单里也不能自批
	if _, err := f.svc.Decide(ctx, f.req, ap.ID, true, ""); !errors.Is(err, approval.ErrSelfApproval) {
		t.Fatalf("self approval: %v", err)
	}
	// 非审批人看不到
	if _, err := f.svc.Decide(ctx, f.other, ap.ID, true, ""); !errors.Is(err, approval.ErrNotFound) {
		t.Fatalf("other: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.other, ap.ID); !errors.Is(err, approval.ErrNotFound) {
		t.Fatalf("other get: %v", err)
	}
	// 指定了审批人时，管理员也不能审批
	if _, err := f.svc.Decide(ctx, f.admin, ap.ID, true, ""); !errors.Is(err, approval.ErrNotFound) {
		t.Fatalf("admin: %v", err)
	}
	// 只有审批人收到提醒（发起人不提醒自己）
	if n := f.c.Notification.Query().Where(notification.Kind(approval.NotifyKind)).CountX(ctx); n != 1 {
		t.Fatalf("notifications %d", n)
	}
	// 并发通过：只执行一次
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.svc.Decide(ctx, f.appr, ap.ID, true, "同意")
		}()
	}
	wg.Wait()
	got, err := f.svc.Get(ctx, f.req, ap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != entapproval.StatusApproved || got.ExecStatus != entapproval.ExecStatusSucceeded || got.Result != "ok:create_ticket" {
		t.Fatalf("approval %+v", got)
	}
	if f.caller.n.Load() != 1 {
		t.Fatalf("calls %d", f.caller.n.Load())
	}
	if f.caller.args[0]["title"] != "打印机坏了" || f.caller.args[0]["api_token"] != "s3cr3t" {
		t.Fatalf("执行参数必须是原快照: %v", f.caller.args[0])
	}
	if _, err := f.svc.RunApproved(ctx, got); err != nil || f.caller.n.Load() != 1 {
		t.Fatal("重复执行")
	}
	if r, ok := approval.ModelResult(got); !ok || r.Text != "ok:create_ticket" {
		t.Fatalf("model result %+v", r)
	}
	// 审计：参数脱敏，带审批单号
	logs := f.c.AuditLog.Query().Where(auditlog.Action("tool.call"), auditlog.PhaseEQ(auditlog.PhaseAttempt)).AllX(ctx)
	if len(logs) != 1 || logs[0].ActorID != f.req.ID || logs[0].Detail["approval_id"] == nil {
		t.Fatalf("tool.call audit %+v", logs)
	}
	if args := logs[0].Detail["args"].(map[string]any); args["api_token"] != "***" || args["title"] != "打印机坏了" {
		t.Fatalf("redact %v", args)
	}
	for _, a := range []string{"approval.create", "approval.approve"} {
		if !f.c.AuditLog.Query().Where(auditlog.Action(a)).ExistX(ctx) {
			t.Fatalf("missing audit %s", a)
		}
	}
}

func TestRejectWithdrawExpire(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	a1 := f.create(t)
	got, err := f.svc.Decide(ctx, f.appr, a1.ID, false, "不需要")
	if err != nil || got.Status != entapproval.StatusRejected {
		t.Fatalf("reject %v %+v", err, got)
	}
	if r, ok := approval.ModelResult(got); !ok || !r.IsError || !strings.Contains(r.Text, "不需要") {
		t.Fatalf("reject result %+v", r)
	}
	if _, err := f.svc.Decide(ctx, f.appr, a1.ID, true, ""); !errors.Is(err, approval.ErrNotPending) {
		t.Fatalf("re-decide %v", err)
	}
	// 撤回：只有发起人
	a2 := f.create(t)
	if _, err := f.svc.Withdraw(ctx, f.appr, a2.ID); !errors.Is(err, approval.ErrNotFound) {
		t.Fatalf("withdraw by approver %v", err)
	}
	if got, err := f.svc.Withdraw(ctx, f.req, a2.ID); err != nil || got.Status != entapproval.StatusWithdrawn {
		t.Fatalf("withdraw %v", err)
	}
	// 超时：读取时实时判定，之后不能再通过
	a3 := f.create(t)
	f.now = f.now.Add(11 * time.Minute)
	got, err = f.svc.Get(ctx, f.appr, a3.ID)
	if err != nil || got.Status != entapproval.StatusExpired {
		t.Fatalf("expire %v %+v", err, got)
	}
	if _, err := f.svc.Decide(ctx, f.appr, a3.ID, true, ""); !errors.Is(err, approval.ErrNotPending) {
		t.Fatalf("approve expired %v", err)
	}
	if r, _ := approval.ModelResult(got); !strings.Contains(r.Text, "超时") {
		t.Fatalf("expire result %+v", r)
	}
	// 定时任务落库
	a4 := f.create(t)
	f.now = f.now.Add(11 * time.Minute)
	if n, err := f.svc.ExpireDue(ctx); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	if f.c.Approval.GetX(ctx, a4.ID).Status != entapproval.StatusExpired {
		t.Fatal("sweep did not expire")
	}
	if f.caller.n.Load() != 0 {
		t.Fatal("拒绝 / 撤回 / 超时都不得执行")
	}
	if !f.c.AuditLog.Query().Where(auditlog.Action("approval.expire")).ExistX(ctx) {
		t.Fatal("expire not audited")
	}
}

func TestEmptyApproversMeansAdmins(t *testing.T) {
	f := setup(t, false)
	ctx := context.Background()
	ap := f.create(t)
	if _, err := f.svc.Decide(ctx, f.appr, ap.ID, true, ""); !errors.Is(err, approval.ErrNotFound) {
		t.Fatalf("non-admin %v", err)
	}
	if got, err := f.svc.Decide(ctx, f.admin, ap.ID, true, ""); err != nil || got.ExecStatus != entapproval.ExecStatusSucceeded {
		t.Fatalf("admin approve %v", err)
	}
}

func TestAuditFailClosed(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	f.w.fail.Store(true)
	if _, err := f.svc.Create(ctx, f.req, approval.CreateInput{Tool: f.tool}); !errors.Is(err, approval.ErrAudit) {
		t.Fatalf("create %v", err)
	}
	if f.c.Approval.Query().CountX(ctx) != 0 {
		t.Fatal("审计失败不得生成审批单")
	}
	f.w.fail.Store(false)
	ap := f.create(t)
	// 通过时执行审计写失败：不调用远程，回到 none，可重试
	f.w.fail.Store(true)
	if _, err := f.c.Approval.UpdateOneID(ap.ID).SetStatus(entapproval.StatusApproved).Save(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.RunApproved(ctx, f.c.Approval.GetX(ctx, ap.ID))
	if !errors.Is(err, approval.ErrAudit) || got.ExecStatus != entapproval.ExecStatusNone || f.caller.n.Load() != 0 {
		t.Fatalf("fail-closed %v %+v calls=%d", err, got, f.caller.n.Load())
	}
	f.w.fail.Store(false)
	got, err = f.svc.RunApproved(ctx, got)
	if err != nil || got.ExecStatus != entapproval.ExecStatusSucceeded || f.caller.n.Load() != 1 {
		t.Fatalf("retry %v %+v", err, got)
	}
}

func TestToolDisabledAfterApproval(t *testing.T) {
	f := setup(t, true)
	ctx := context.Background()
	ap := f.create(t)
	f.c.Tool.UpdateOneID(f.tool.ID).SetEnabled(false).ExecX(ctx)
	got, err := f.svc.Decide(ctx, f.appr, ap.ID, true, "")
	if err != nil || got.ExecStatus != entapproval.ExecStatusFailed || !got.ResultIsError || f.caller.n.Load() != 0 {
		t.Fatalf("disabled %v %+v", err, got)
	}
}

func TestRedact(t *testing.T) {
	in := map[string]any{"Password": "x", "X-Api-Key": "y", "monkey": "ok", "nested": []any{map[string]any{"authorization": "z"}},
		"long": strings.Repeat("啊", 600)}
	out := approval.Redact(in).(map[string]any)
	if out["Password"] != "***" || out["X-Api-Key"] != "***" || out["monkey"] != "ok" {
		t.Fatalf("%v", out)
	}
	if out["nested"].([]any)[0].(map[string]any)["authorization"] != "***" {
		t.Fatal("nested")
	}
	if len([]rune(out["long"].(string))) != 501 {
		t.Fatal("truncate")
	}
}
