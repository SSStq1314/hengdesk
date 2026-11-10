package budget_test

import (
	"context"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/budget"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	entbudget "github.com/SSStq1314/hengdesk/server/internal/ent/budget"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

// TestDBUsedSQLiteWindowAroundShanghai1600 覆盖 SQLite 用进程本地墙钟存 datetime、
// 预算周期按 Asia/Shanghai 切开时的比较。#55 曾把 CI 上 used=0 误判为 Redis 断连，
// 实际是 TZ=UTC 下用 +08:00 边界比较漏计；后来 DBUsed 改成 .UTC() 边界，又会在
// TZ=Asia/Shanghai 且当地时间 ≥ 16:00（UTC 日界后 8 小时）把当天用量漏计为 0。
func TestDBUsedSQLiteWindowAroundShanghai1600(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	// 覆盖上海日切前后，以及 16:00 这个与 UTC 墙钟对撞的时刻。
	cases := []time.Time{
		time.Date(2026, 11, 10, 0, 30, 0, 0, loc),
		time.Date(2026, 11, 10, 15, 59, 0, 0, loc),
		time.Date(2026, 11, 10, 16, 0, 0, 0, loc),
		time.Date(2026, 11, 10, 16, 12, 0, 0, loc),
		time.Date(2026, 11, 10, 23, 59, 0, 0, loc),
	}
	for _, now := range cases {
		t.Run(now.Format("15:04"), func(t *testing.T) {
			ctx := context.Background()
			c := storetest.NewTestClient(t)
			org := c.Organization.Create().SetName("o").SetSlug("o").SaveX(ctx)
			// 与生产 Default(time.Now) 一样按进程本地墙钟写入。
			c.UsageRecord.Create().
				SetOrgID(org.ID).SetUserID(1).SetDepartmentID(1).SetModelID(1).
				SetModelName("m").SetProviderID(1).SetTotalTokens(12).
				SetCreatedAt(now.In(time.Local)).SaveX(ctx)
			// 窗口外：前一上海日同一墙钟，不应计入。
			c.UsageRecord.Create().
				SetOrgID(org.ID).SetUserID(1).SetDepartmentID(1).SetModelID(1).
				SetModelName("m").SetProviderID(1).SetTotalTokens(99).
				SetCreatedAt(now.Add(-24 * time.Hour).In(time.Local)).SaveX(ctx)
			b := &ent.Budget{OrgID: org.ID, Scope: entbudget.ScopeOrg, Metric: entbudget.MetricTokens}
			w := budget.PeriodWindow(entbudget.PeriodDay, now, loc)
			used, err := (&budget.Service{DB: c, Loc: loc, Now: func() time.Time { return now }}).DBUsed(ctx, b, w)
			if err != nil {
				t.Fatal(err)
			}
			if used != 12 {
				t.Fatalf("当天用量应计入 12，got %d（进程 TZ=%s，window=%s .. %s，key=%s）",
					used, time.Local, w.Start.Format(time.RFC3339), w.End.Format(time.RFC3339), w.Key)
			}
		})
	}
}
