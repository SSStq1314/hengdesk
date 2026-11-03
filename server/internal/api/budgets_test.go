package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/budget"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/notification"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

type budgetFixture struct {
	*gwEnv
	root, child *ent.Department
	dev, da     *ent.User
}

func newBudgetFixture(t *testing.T, opts ...func(*api.Deps)) *budgetFixture {
	g := newGW(t, false, opts...)
	ctx := context.Background()
	org := g.c.Organization.Query().OnlyX(ctx)
	tx, _ := g.c.Tx(ctx)
	root, _ := store.CreateDept(ctx, tx, org.ID, "研发部", nil)
	child, _ := store.CreateDept(ctx, tx, org.ID, "后端组", &root.ID)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f := &budgetFixture{gwEnv: g, root: root, child: child}
	f.dev = g.addUser(t, "dev@x.local", org, rbac.Member, child)
	f.da = g.addUser(t, "da@x.local", org, rbac.DeptAdmin, root)
	m := g.c.Model.Query().OnlyX(ctx)
	g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/acl", m.ID), fmt.Sprintf(`{"subject_type":"user","subject_id":%d}`, f.dev.ID), true)
	g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/default", m.ID), "", true)
	return f
}

const hi = `{"messages":[{"role":"user","content":"hi"}]}`

func TestBudgetBlockAlertReconcile(t *testing.T) {
	f := newBudgetFixture(t)
	ctx := context.Background()
	dev := f.loginAs(t, "dev@x.local", "password123")
	org := f.c.Organization.Query().OnlyX(ctx)
	f.addUser(t, "orgadmin@x.local", org, rbac.OrgAdmin, nil)

	// 非预算管理员：一律 404
	for _, who := range []string{"dev@x.local", "da@x.local", "orgadmin@x.local"} {
		cl := f.loginAs(t, who, "password123")
		for _, r := range [][2]string{{http.MethodGet, "/api/v1/admin/budgets"}, {http.MethodPost, "/api/v1/admin/budgets"},
			{http.MethodPut, "/api/v1/admin/budgets/1"}, {http.MethodDelete, "/api/v1/admin/budgets/1"}, {http.MethodPost, "/api/v1/admin/budgets/reconcile"}} {
			if got := cl.do(r[0], r[1], `{}`, r[0] != http.MethodGet).Code; got != 404 {
				t.Errorf("%s %s %s → %d", who, r[0], r[1], got)
			}
		}
	}
	// 参数校验
	if got := f.admin.do(http.MethodPost, "/api/v1/admin/budgets", `{"scope":"dept","scope_id":99999,"period":"day","metric":"tokens","limit":1}`, true).Code; got != 400 {
		t.Fatalf("bad dept %d", got)
	}
	if got := f.admin.do(http.MethodPost, "/api/v1/admin/budgets", `{"scope":"dept","scope_id":1,"period":"week","metric":"tokens","limit":1}`, true).Code; got != 400 {
		t.Fatalf("bad period %d", got)
	}
	// 没有预算时正常调用
	if rec := dev.do(http.MethodPost, "/api/v1/chat/completions", hi, true); rec.Code != 200 {
		t.Fatalf("chat %d %s", rec.Code, rec.Body)
	}
	// 研发部（含下级）日 token 预算：已用量来自 usage_records 重建，本次调用前已超
	body := fmt.Sprintf(`{"scope":"dept","scope_id":%d,"period":"day","metric":"tokens","limit":5}`, f.root.ID)
	rec := f.admin.do(http.MethodPost, "/api/v1/admin/budgets", body, true)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"threshold_pct":80`) || !strings.Contains(rec.Body.String(), `"action":"block"`) {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	var bv struct {
		ID   int
		Used int64
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &bv)
	if bv.Used == 0 {
		t.Fatalf("应从 usage_records 重建已用量: %s", rec.Body)
	}
	if got := f.admin.do(http.MethodPost, "/api/v1/admin/budgets", body, true).Code; got != 409 {
		t.Fatalf("dup %d", got)
	}
	rec = dev.do(http.MethodPost, "/api/v1/chat/completions", hi, true)
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), `"code":"budget_exceeded"`) || !strings.Contains(rec.Body.String(), "部门预算已用尽") {
		t.Fatalf("应拦截 %d %s", rec.Code, rec.Body)
	}
	if n := f.c.AuditLog.Query().Where(auditlog.Action("model.call"), auditlog.Outcome("denied")).CountX(ctx); n != 1 {
		t.Fatalf("拦截审计 %d", n)
	}
	// 管理员本人不在研发部：不受影响；调用会触发组织级以外的结算，不影响研发部计数
	if rec := f.admin.do(http.MethodPost, "/api/v1/chat/completions", hi, true); rec.Code != 200 {
		t.Fatalf("admin chat %d", rec.Code)
	}

	// 仅告警：放行，结算后越过 100% → 部门管理员与预算管理员收到站内提醒（各一次），并记审计
	rec = f.admin.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/budgets/%d", bv.ID),
		fmt.Sprintf(`{"scope":"dept","scope_id":%d,"period":"day","metric":"tokens","limit":5,"action":"alert_only"}`, f.root.ID), true)
	if rec.Code != 200 {
		t.Fatalf("update %d %s", rec.Code, rec.Body)
	}
	for i := 0; i < 2; i++ {
		if rec := dev.do(http.MethodPost, "/api/v1/chat/completions", `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`, true); rec.Code != 200 {
			t.Fatalf("alert_only stream %d %s", rec.Code, rec.Body)
		}
	}
	if n := f.c.Notification.Query().Where(notification.UserID(f.da.ID)).CountX(ctx); n != 1 {
		t.Fatalf("部门管理员提醒 %d", n)
	}
	adminID := atoi(f.uid(t, "admin@x.local"))
	if n := f.c.Notification.Query().Where(notification.UserID(adminID)).CountX(ctx); n != 1 {
		t.Fatalf("预算管理员提醒 %d", n)
	}
	if n := f.c.Notification.Query().Where(notification.UserID(f.dev.ID)).CountX(ctx); n != 0 {
		t.Fatalf("部门预算不提醒普通成员 %d", n)
	}
	if n := f.c.AuditLog.Query().Where(auditlog.Action(budget.AuditAlert)).CountX(ctx); n != 1 {
		t.Fatalf("告警审计 %d", n)
	}
	da := f.loginAs(t, "da@x.local", "password123")
	if rec := da.do(http.MethodGet, "/api/v1/notifications", "", false); !strings.Contains(rec.Body.String(), `"unread":1`) || !strings.Contains(rec.Body.String(), "预算已用尽") {
		t.Fatalf("notifications %s", rec.Body)
	}
	da.do(http.MethodPost, "/api/v1/notifications/read", "", true)
	if rec := da.do(http.MethodGet, "/api/v1/notifications", "", false); !strings.Contains(rec.Body.String(), `"unread":0`) {
		t.Fatalf("read %s", rec.Body)
	}
	if rec := dev.do(http.MethodGet, "/api/v1/budgets/me", "", false); rec.Code != 200 || !strings.Contains(rec.Body.String(), "研发部") {
		t.Fatalf("me %d %s", rec.Code, rec.Body)
	}

	// 对账：篡改计数 → 对账发现漂移并以库为准重置
	keys := f.mr.Keys()
	var ck string
	for _, k := range keys {
		if strings.HasPrefix(k, fmt.Sprintf("budget:used:%d:", bv.ID)) {
			ck = k
		}
	}
	if ck == "" {
		t.Fatalf("缺少计数 key: %v", keys)
	}
	dbVal, _ := f.mr.Get(ck)
	_ = f.mr.Set(ck, "1")
	rec = f.admin.do(http.MethodPost, "/api/v1/admin/budgets/reconcile", "", true)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"drift":0`) {
		t.Fatalf("reconcile %d %s", rec.Code, rec.Body)
	}
	if v, _ := f.mr.Get(ck); v != dbVal {
		t.Fatalf("对账后 %s != %s", v, dbVal)
	}
	// 计数丢失 → 下次读取从 usage_records 重建
	f.mr.Del(ck)
	f.admin.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/budgets/%d", bv.ID),
		fmt.Sprintf(`{"scope":"dept","scope_id":%d,"period":"day","metric":"tokens","limit":5}`, f.root.ID), true)
	if rec := dev.do(http.MethodPost, "/api/v1/chat/completions", hi, true); rec.Code != 429 {
		t.Fatalf("重建后应拦截 %d", rec.Code)
	}
	if got := f.admin.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/budgets/%d", bv.ID), "", true).Code; got != 204 {
		t.Fatalf("delete %d", got)
	}
	if rec := dev.do(http.MethodPost, "/api/v1/chat/completions", hi, true); rec.Code != 200 {
		t.Fatalf("删除后放行 %d", rec.Code)
	}
	for _, a := range []string{"budget.create", "budget.update", "budget.delete", "budget.reconcile"} {
		if n := f.c.AuditLog.Query().Where(auditlog.Action(a)).CountX(ctx); n == 0 {
			t.Errorf("缺少审计 %s", a)
		}
	}
}

// Valkey 不可用：存在适用预算时 fail-closed；没有预算时不受影响。
func TestBudgetFailClosed(t *testing.T) {
	dead := miniredis.RunT(t)
	addr := dead.Addr()
	dead.Close()
	f := newBudgetFixture(t, func(d *api.Deps) {
		d.Budget = &budget.Service{DB: d.DB, R: redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})}
	})
	dev := f.loginAs(t, "dev@x.local", "password123")
	if rec := dev.do(http.MethodPost, "/api/v1/chat/completions", hi, true); rec.Code != 200 {
		t.Fatalf("无预算 %d %s", rec.Code, rec.Body)
	}
	rec := f.admin.do(http.MethodPost, "/api/v1/admin/budgets", `{"scope":"org","period":"month","metric":"cost","limit":100000000}`, true)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"counter_unavailable":true`) {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	rec = dev.do(http.MethodPost, "/api/v1/chat/completions", hi, true)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "budget_unavailable") {
		t.Fatalf("应 fail-closed %d %s", rec.Code, rec.Body)
	}
}
