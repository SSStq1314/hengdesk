package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/store"
	"github.com/SSStq1314/hengdesk/server/internal/usage"
)

func TestStatsOverviewScopeExport(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	org := g.c.Organization.Query().OnlyX(ctx)
	tx, _ := g.c.Tx(ctx)
	root, _ := store.CreateDept(ctx, tx, org.ID, "研发部", nil)
	other, _ := store.CreateDept(ctx, tx, org.ID, "市场部", nil)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	dev := g.addUser(t, "dev@x.local", org, rbac.Member, root)
	g.addUser(t, "da@x.local", org, rbac.DeptAdmin, root)
	m := g.c.Model.Query().OnlyX(ctx)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	deptID := root.ID
	otherID := other.ID
	// 本部门用量
	g.c.UsageRecord.Create().SetOrgID(org.ID).SetUserID(dev.ID).SetDepartmentID(deptID).SetModelID(m.ID).
		SetModelName(m.Name).SetProviderID(m.ProviderID).SetTotalTokens(100).SetCostMicros(1000).
		SetPromptTokens(40).SetCompletionTokens(60).SetCreatedAt(now).SaveX(ctx)
	// 其他部门用量
	mktUser := g.addUser(t, "mk2@x.local", org, rbac.Member, other)
	g.c.UsageRecord.Create().SetOrgID(org.ID).SetUserID(mktUser.ID).SetDepartmentID(otherID).SetModelID(m.ID).
		SetModelName(m.Name).SetProviderID(m.ProviderID).SetTotalTokens(999).SetCostMicros(9999).
		SetCreatedAt(now).SaveX(ctx)
	g.c.Conversation.Create().SetOrgID(org.ID).SetUserID(dev.ID).SetTitle("t").SaveX(ctx)

	// 普通成员 404
	mem := g.loginAs(t, "dev@x.local", "password123")
	for _, u := range []string{"/api/v1/admin/stats/overview", "/api/v1/admin/stats/summary", "/api/v1/admin/stats/timeseries", "/api/v1/admin/stats/export"} {
		if got := mem.do(http.MethodGet, u, "", false).Code; got != 404 {
			t.Fatalf("member %s → %d", u, got)
		}
	}

	// 部门管理员只看本部门
	dac := g.loginAs(t, "da@x.local", "password123")
	rec := dac.do(http.MethodGet, "/api/v1/admin/stats/overview", "", false)
	if rec.Code != 200 {
		t.Fatalf("da overview %d %s", rec.Code, rec.Body)
	}
	var ov struct {
		Totals        usage.Totals `json:"totals"`
		ActiveUsers   int          `json:"active_users"`
		Conversations int          `json:"conversations"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ov)
	if ov.Totals.TotalTokens != 100 || ov.ActiveUsers != 1 {
		t.Fatalf("da scoped overview %+v", ov)
	}

	// 组织管理员看全部
	rec = g.admin.do(http.MethodGet, "/api/v1/admin/stats/overview", "", false)
	_ = json.Unmarshal(rec.Body.Bytes(), &ov)
	if ov.Totals.TotalTokens != 1099 {
		t.Fatalf("admin overview %+v", ov)
	}

	rec = g.admin.do(http.MethodGet, "/api/v1/admin/stats/summary?group_by=department", "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "研发部") {
		t.Fatalf("summary %d %s", rec.Code, rec.Body)
	}

	rec = g.admin.do(http.MethodGet, "/api/v1/admin/stats/timeseries?grain=day&group_by=total", "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"points"`) {
		t.Fatalf("timeseries %d %s", rec.Code, rec.Body)
	}

	// 导出记审计
	rec = g.admin.do(http.MethodGet, "/api/v1/admin/stats/export", "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "total_tokens") {
		t.Fatalf("export %d %s", rec.Code, rec.Body)
	}
	if n := g.c.AuditLog.Query().Where(auditlog.Action("usage.export")).CountX(ctx); n < 1 {
		t.Fatalf("export audit %d", n)
	}

	// 小时聚合后 timeseries 仍可读
	if _, err := usage.RebuildRecent(ctx, g.c, loc, now); err != nil {
		t.Fatal(err)
	}
	rec = g.admin.do(http.MethodGet, "/api/v1/admin/stats/timeseries?grain=hour&group_by=model", "", false)
	if rec.Code != 200 {
		t.Fatalf("hourly ts %d %s", rec.Code, rec.Body)
	}
}

func TestUsageSummaryBudgetRead(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	org := g.c.Organization.Query().OnlyX(ctx)
	g.addUser(t, "orgadmin@x.local", org, rbac.OrgAdmin, nil)
	oa := g.loginAs(t, "orgadmin@x.local", "password123")
	// 组织管理员无 ModelManage，但有 BudgetRead，应能看用量
	if got := oa.do(http.MethodGet, "/api/v1/admin/usage/summary?group_by=total", "", false).Code; got != 200 {
		t.Fatalf("orgadmin usage summary %d", got)
	}
}
