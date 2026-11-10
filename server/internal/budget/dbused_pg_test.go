package budget_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/budget"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	entbudget "github.com/SSStq1314/hengdesk/server/internal/ent/budget"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

// TestPGDBUsedWindowAroundShanghaiAfternoon 覆盖生产路径：usage_records.created_at
// 是 timestamptz（见 20261106000000_m1_model_usage.sql）。SQLite 把 datetime 存成
// 进程本地墙钟，在 TZ=Asia/Shanghai 且当地 ≥ 16:00 时曾把当天用量漏计（#75）；
// PostgreSQL 按瞬时比较，理论上不应受墙钟影响。本用例用固定时钟模拟上海
// 16:00–24:00，确认 DBUsed 仍计入当天、排除前一日。无 PG 时跳过
// （HENGDESK_PG_IT=1 HENGDESK_TEST_DATABASE_URL=...，会清空该库 public schema）。
func TestPGDBUsedWindowAroundShanghaiAfternoon(t *testing.T) {
	if os.Getenv("HENGDESK_PG_IT") != "1" {
		t.Skip("set HENGDESK_PG_IT=1")
	}
	c, db, err := store.Open(os.Getenv("HENGDESK_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if _, err := db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	// 固定时钟：覆盖 16:00 这个与 UTC 墙钟对撞的时刻，以及整个下午到日切前。
	cases := []time.Time{
		time.Date(2026, 11, 10, 16, 0, 0, 0, loc),
		time.Date(2026, 11, 10, 16, 12, 0, 0, loc),
		time.Date(2026, 11, 10, 20, 0, 0, 0, loc),
		time.Date(2026, 11, 10, 23, 59, 0, 0, loc),
	}
	for _, now := range cases {
		t.Run(now.Format("15:04"), func(t *testing.T) {
			slug := fmt.Sprintf("pg-%s", now.Format("1504"))
			org := c.Organization.Create().SetName(slug).SetSlug(slug).SaveX(ctx)
			c.UsageRecord.Create().
				SetOrgID(org.ID).SetUserID(1).SetDepartmentID(1).SetModelID(1).
				SetModelName("m").SetProviderID(1).SetTotalTokens(12).
				SetCreatedAt(now).SaveX(ctx)
			c.UsageRecord.Create().
				SetOrgID(org.ID).SetUserID(1).SetDepartmentID(1).SetModelID(1).
				SetModelName("m").SetProviderID(1).SetTotalTokens(99).
				SetCreatedAt(now.Add(-24 * time.Hour)).SaveX(ctx)

			b := &ent.Budget{OrgID: org.ID, Scope: entbudget.ScopeOrg, Metric: entbudget.MetricTokens}
			w := budget.PeriodWindow(entbudget.PeriodDay, now, loc)
			used, err := (&budget.Service{DB: c, Loc: loc, Now: func() time.Time { return now }}).DBUsed(ctx, b, w)
			if err != nil {
				t.Fatal(err)
			}
			if used != 12 {
				t.Fatalf("PG timestamptz 当天用量应计入 12，got %d（进程 TZ=%s，window=%s .. %s，key=%s）",
					used, time.Local, w.Start.Format(time.RFC3339), w.End.Format(time.RFC3339), w.Key)
			}
		})
	}
}
