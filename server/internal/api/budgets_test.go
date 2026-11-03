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

	// 普通成员：一律 404；部门管理员只读（写接口 404）；组织管理员可管理（D-37 第 9 条）
	writes := [][2]string{{http.MethodPost, "/api/v1/admin/budgets"}, {http.MethodPut, "/api/v1/admin/budgets/1"},
		{http.MethodDelete, "/api/v1/admin/budgets/1"}, {http.MethodPost, "/api/v1/admin/budgets/reconcile"}}
	for _, who := range []string{"dev@x.local", "da@x.local"} {
		cl := f.loginAs(t, who, "password123")
		rs := writes
		if who == "dev@x.local" {
			rs = append(rs, [2]string{http.MethodGet, "/api/v1/admin/budgets"}, [2]string{http.MethodGet, "/api/v1/admin/budgets/1"})
		}
		for _, r := range rs {
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

func budgetID(t *testing.T, rec interface{ Bytes() []byte }) int {
	t.Helper()
	var v struct{ ID int }
	_ = json.Unmarshal(rec.Bytes(), &v)
	if v.ID == 0 {
		t.Fatalf("no id: %s", rec.Bytes())
	}
	return v.ID
}

// D-37 第 9 条：部门管理员只读本部门及下级的部门预算和成员个人预算，范围外 404；组织管理员可管理。
func TestBudgetDeptAdminScope(t *testing.T) {
	f := newBudgetFixture(t)
	ctx := context.Background()
	org := f.c.Organization.Query().OnlyX(ctx)
	tx, _ := f.c.Tx(ctx)
	sales, _ := store.CreateDept(ctx, tx, org.ID, "销售部", nil)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.addUser(t, "oa@x.local", org, rbac.OrgAdmin, nil)
	oa := f.loginAs(t, "oa@x.local", "password123")
	mk := func(body string) int {
		rec := oa.do(http.MethodPost, "/api/v1/admin/budgets", body, true)
		if rec.Code != 201 {
			t.Fatalf("组织管理员创建 %d %s", rec.Code, rec.Body)
		}
		return budgetID(t, rec.Body)
	}
	child := mk(fmt.Sprintf(`{"scope":"dept","scope_id":%d,"period":"month","metric":"tokens","limit":100000}`, f.child.ID))
	self := mk(fmt.Sprintf(`{"scope":"user","scope_id":%d,"period":"month","metric":"tokens","limit":100000}`, f.dev.ID))
	out := mk(fmt.Sprintf(`{"scope":"dept","scope_id":%d,"period":"month","metric":"tokens","limit":100000}`, sales.ID))
	orgB := mk(`{"scope":"org","period":"month","metric":"tokens","limit":100000000}`)

	da := f.loginAs(t, "da@x.local", "password123")
	rec := da.do(http.MethodGet, "/api/v1/admin/budgets", "", false)
	var vs []struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &vs)
	if rec.Code != 200 || len(vs) != 2 {
		t.Fatalf("部门管理员列表 %d %s", rec.Code, rec.Body)
	}
	for _, id := range []int{child, self} {
		if got := da.do(http.MethodGet, fmt.Sprintf("/api/v1/admin/budgets/%d", id), "", false).Code; got != 200 {
			t.Errorf("范围内 %d → %d", id, got)
		}
		if got := da.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/budgets/%d", id), `{}`, true).Code; got != 404 {
			t.Errorf("只读却能改 %d → %d", id, got)
		}
	}
	for _, id := range []int{out, orgB, 99999} {
		if got := da.do(http.MethodGet, fmt.Sprintf("/api/v1/admin/budgets/%d", id), "", false).Code; got != 404 {
			t.Errorf("范围外 %d → %d", id, got)
		}
	}
	if got := oa.do(http.MethodGet, fmt.Sprintf("/api/v1/admin/budgets/%d", out), "", false).Code; got != 200 {
		t.Errorf("组织管理员读 %d", got)
	}
	if got := oa.do(http.MethodPost, "/api/v1/admin/budgets/reconcile", "", true).Code; got != 200 {
		t.Errorf("组织管理员对账 %d", got)
	}
}

// D-37 第 11、13 条：告警状态入库，计数重建后不重发；修改预算立即重建并按新级别重新判断。
func TestBudgetAlertStateAndRebuild(t *testing.T) {
	f := newBudgetFixture(t)
	ctx := context.Background()
	dev := f.loginAs(t, "dev@x.local", "password123")
	stream := `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	if rec := dev.do(http.MethodPost, "/api/v1/chat/completions", hi, true); rec.Code != 200 {
		t.Fatalf("chat %d", rec.Code)
	}
	body := func(limit int) string {
		return fmt.Sprintf(`{"scope":"dept","scope_id":%d,"period":"day","metric":"tokens","limit":%d,"action":"alert_only"}`, f.root.ID, limit)
	}
	rec := f.admin.do(http.MethodPost, "/api/v1/admin/budgets", body(5), true)
	if rec.Code != 201 {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	id := budgetID(t, rec.Body)
	dropCounters := func() { // 模拟 Valkey 丢失预算计数（会话也在 Valkey，不整体清空）
		for _, k := range f.mr.Keys() {
			if strings.HasPrefix(k, "budget:") {
				f.mr.Del(k)
			}
		}
	}
	daN := func() int { return f.c.Notification.Query().Where(notification.UserID(f.da.ID)).CountX(ctx) }
	// 新建时已超额：立即告警一次
	if n := daN(); n != 1 {
		t.Fatalf("新建即告警 %d", n)
	}
	// Valkey 全部丢失（重启）→ 计数重建，同一周期同一级别不重发
	dropCounters()
	if rec := dev.do(http.MethodPost, "/api/v1/chat/completions", stream, true); rec.Code != 200 {
		t.Fatalf("chat %d", rec.Code)
	}
	if n := daN(); n != 1 {
		t.Fatalf("重建后重发了 %d", n)
	}
	// 修改额度：立即重建计数（不等下次调用）；级别回落 → 清掉「已用尽」状态
	before := len(f.mr.Keys())
	dropCounters()
	if rec := f.admin.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/budgets/%d", id), body(100000000), true); rec.Code != 200 {
		t.Fatalf("update %d %s", rec.Code, rec.Body)
	}
	found := false
	for _, k := range f.mr.Keys() {
		if strings.HasPrefix(k, fmt.Sprintf("budget:used:%d:", id)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("修改后未立即重建计数（之前 %d 个 key）", before)
	}
	if n := f.c.BudgetAlertState.Query().CountX(ctx); n != 0 {
		t.Fatalf("级别回落应清掉状态 %d", n)
	}
	// 改回小额度：级别再次变化 → 重新告警
	if rec := f.admin.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/budgets/%d", id), body(5), true); rec.Code != 200 {
		t.Fatalf("update %d", rec.Code)
	}
	if n := daN(); n != 2 {
		t.Fatalf("级别变化应重新告警 %d", n)
	}
	// 组织管理员也收到告警（D-37 第 9 条）
	org := f.c.Organization.Query().OnlyX(ctx)
	oa := f.addUser(t, "oa@x.local", org, rbac.OrgAdmin, nil)
	f.admin.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/budgets/%d", id), body(100000000), true)
	f.admin.do(http.MethodPut, fmt.Sprintf("/api/v1/admin/budgets/%d", id), body(5), true)
	if n := f.c.Notification.Query().Where(notification.UserID(oa.ID)).CountX(ctx); n != 1 {
		t.Fatalf("组织管理员告警 %d", n)
	}
	if got := f.admin.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/budgets/%d", id), "", true).Code; got != 204 {
		t.Fatalf("delete %d", got)
	}
	if n := f.c.BudgetAlertState.Query().CountX(ctx); n != 0 {
		t.Fatalf("删除预算后状态残留 %d", n)
	}
}

// D-37 第 4 条：测试连接计入用量，但不受超额拦截。
func TestBudgetTestConnectionNotBlocked(t *testing.T) {
	f := newBudgetFixture(t)
	ctx := context.Background()
	if rec := f.admin.do(http.MethodPost, "/api/v1/chat/completions", hi, true); rec.Code != 200 {
		t.Fatalf("chat %d", rec.Code)
	}
	if rec := f.admin.do(http.MethodPost, "/api/v1/admin/budgets", `{"scope":"org","period":"day","metric":"tokens","limit":1}`, true); rec.Code != 201 {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	if rec := f.admin.do(http.MethodPost, "/api/v1/chat/completions", hi, true); rec.Code != 429 {
		t.Fatalf("对话应被拦截 %d", rec.Code)
	}
	n0 := f.c.UsageRecord.Query().CountX(ctx)
	m := f.c.Model.Query().OnlyX(ctx)
	rec := f.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/test", m.ID), "", true)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "budget_exceeded") {
		t.Fatalf("测试连接不应被拦截 %d %s", rec.Code, rec.Body)
	}
	if n := f.c.UsageRecord.Query().CountX(ctx); n != n0+1 {
		t.Fatalf("测试连接应计入用量 %d → %d", n0, n)
	}
}
