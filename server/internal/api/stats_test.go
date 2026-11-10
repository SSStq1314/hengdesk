package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
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

	// 导出记审计（含筛选条件与行数，D-46 第 3 条）
	rec = g.admin.do(http.MethodGet, "/api/v1/admin/stats/export?from=2020-01-01&to=2099-12-31", "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "total_tokens") {
		t.Fatalf("export %d %s", rec.Code, rec.Body)
	}
	ex := g.c.AuditLog.Query().Where(auditlog.Action("usage.export"), auditlog.PhaseEQ(auditlog.PhaseResult)).Order(ent.Desc(auditlog.FieldID)).FirstX(ctx)
	if ex.Detail["rows"] == nil {
		t.Fatalf("export audit missing rows: %+v", ex.Detail)
	}
	raw, _ := json.Marshal(ex.Detail)
	if !strings.Contains(string(raw), `"rows"`) || !strings.Contains(string(raw), `"from"`) || !strings.Contains(string(raw), `"to"`) {
		t.Fatalf("export audit filters: %s", raw)
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

func TestD46UsageDetailAuditAndDayBoundary(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	g := newGW(t, false, func(d *api.Deps) { d.Timezone = loc })
	ctx := context.Background()
	org := g.c.Organization.Query().OnlyX(ctx)
	m := g.c.Model.Query().OnlyX(ctx)
	dev := g.addUser(t, "dev2@x.local", org, rbac.Member, nil)
	// 日界：2026-11-10 00:30 上海 = 2026-11-09 16:30 UTC
	ts := time.Date(2026, 11, 10, 0, 30, 0, 0, loc)
	g.c.UsageRecord.Create().SetOrgID(org.ID).SetUserID(dev.ID).SetModelID(m.ID).
		SetModelName(m.Name).SetProviderID(m.ProviderID).SetTotalTokens(42).SetCostMicros(100).
		SetPromptTokens(20).SetCompletionTokens(22).SetCreatedAt(ts).SaveX(ctx)

	// 按系统时区切日：11-10 含这条，11-09 不含
	rec := g.admin.do(http.MethodGet, "/api/v1/admin/stats/overview?from=2026-11-10&to=2026-11-10", "", false)
	if rec.Code != 200 {
		t.Fatalf("overview 11-10 %d %s", rec.Code, rec.Body)
	}
	var ov struct {
		Totals usage.Totals `json:"totals"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ov)
	if ov.Totals.TotalTokens != 42 {
		t.Fatalf("shanghai 11-10 should include 00:30 record: %+v %s", ov, rec.Body)
	}
	rec = g.admin.do(http.MethodGet, "/api/v1/admin/stats/overview?from=2026-11-09&to=2026-11-09", "", false)
	_ = json.Unmarshal(rec.Body.Bytes(), &ov)
	if ov.Totals.TotalTokens != 0 {
		t.Fatalf("shanghai 11-09 should exclude 00:30 record: %+v %s", ov, rec.Body)
	}

	if _, err := usage.RebuildHours(ctx, g.c, loc, ts.Add(-time.Hour), ts.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rec = g.admin.do(http.MethodGet, "/api/v1/admin/stats/timeseries?grain=day&group_by=total&from=2026-11-10&to=2026-11-10", "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"2026-11-10"`) {
		t.Fatalf("timeseries bucket %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), `"2026-11-09"`) {
		t.Fatalf("should not bucket into UTC date 11-09: %s", rec.Body)
	}

	adminID := g.c.User.Query().Where(user.Email("admin@x.local")).OnlyX(ctx).ID
	before := g.c.AuditLog.Query().Where(auditlog.Action("usage.detail.read")).CountX(ctx)
	rec = g.admin.do(http.MethodGet, "/api/v1/admin/usage/records?user_id="+strconv.Itoa(dev.ID), "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total_tokens":42`) {
		t.Fatalf("records %d %s", rec.Code, rec.Body)
	}
	after := g.c.AuditLog.Query().Where(auditlog.Action("usage.detail.read"), auditlog.PhaseEQ(auditlog.PhaseResult)).CountX(ctx)
	if after <= before {
		t.Fatal("missing usage.detail.read audit")
	}
	lg := g.c.AuditLog.Query().Where(auditlog.Action("usage.detail.read"), auditlog.PhaseEQ(auditlog.PhaseResult)).Order(ent.Desc(auditlog.FieldID)).FirstX(ctx)
	raw, _ := json.Marshal(lg.Detail)
	if !strings.Contains(string(raw), `"rows"`) || !strings.Contains(string(raw), strconv.Itoa(dev.ID)) {
		t.Fatalf("detail audit %s", raw)
	}

	before = g.c.AuditLog.Query().Where(auditlog.Action("usage.detail.read")).CountX(ctx)
	rec = g.admin.do(http.MethodGet, "/api/v1/admin/usage/records?user_id="+strconv.Itoa(adminID), "", false)
	if rec.Code != 200 {
		t.Fatalf("own records %d %s", rec.Code, rec.Body)
	}
	if got := g.c.AuditLog.Query().Where(auditlog.Action("usage.detail.read")).CountX(ctx); got != before {
		t.Fatalf("own detail should not audit, %d -> %d", before, got)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/usage/records", nil)
	g.h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("anon records %d", rec.Code)
	}
	mem := g.loginAs(t, "dev2@x.local", "password123")
	if got := mem.do(http.MethodGet, "/api/v1/admin/usage/records", "", false).Code; got != 404 {
		t.Fatalf("member records %d", got)
	}
}
