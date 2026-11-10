package usage_test

import (
	"context"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/ent/usagerecord"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
	"github.com/SSStq1314/hengdesk/server/internal/usage"
)

func TestRebuildHours(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-10 10:30 Asia/Shanghai
	ts := time.Date(2026, 11, 10, 10, 30, 0, 0, loc)
	dept := 3
	agent := 7
	_, err = c.UsageRecord.Create().SetOrgID(1).SetUserID(2).SetDepartmentID(dept).SetModelID(4).SetModelName("m").
		SetProviderID(5).SetPromptTokens(10).SetCompletionTokens(20).SetTotalTokens(30).SetCostMicros(100).
		SetAgentID(agent).SetCreatedAt(ts).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.UsageRecord.Create().SetOrgID(1).SetUserID(2).SetDepartmentID(dept).SetModelID(4).SetModelName("m").
		SetProviderID(5).SetPromptTokens(1).SetCompletionTokens(2).SetTotalTokens(3).SetCostMicros(10).
		SetEstimated(true).SetAgentID(agent).SetCreatedAt(ts.Add(20 * time.Minute)).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, err := usage.RebuildHours(ctx, c, loc, ts.Add(-time.Hour), ts.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("rebuild n=%d err=%v", n, err)
	}
	rows := c.UsageHourly.Query().AllX(ctx)
	if len(rows) != 1 {
		t.Fatalf("rows %d", len(rows))
	}
	r := rows[0]
	if r.Calls != 2 || r.TotalTokens != 33 || r.CostMicros != 110 || r.EstimatedCalls != 1 || r.AgentID != 7 || r.DepartmentID != 3 {
		t.Fatalf("agg %+v", r)
	}
	wantBucket := usage.HourBucket(ts, loc)
	if !r.BucketAt.Equal(wantBucket) {
		t.Fatalf("bucket %v want %v", r.BucketAt, wantBucket)
	}
	// 幂等：再跑一次行数不变
	n, err = usage.RebuildHours(ctx, c, loc, ts.Add(-time.Hour), ts.Add(time.Hour))
	if err != nil || n != 1 || c.UsageHourly.Query().CountX(ctx) != 1 {
		t.Fatalf("idempotent n=%d count=%d err=%v", n, c.UsageHourly.Query().CountX(ctx), err)
	}
	_ = usagerecord.StatusOk
}

func TestSummaryByAgent(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	aid := 9
	_, err := c.UsageRecord.Create().SetOrgID(1).SetUserID(1).SetModelID(1).SetModelName("m").SetProviderID(1).
		SetTotalTokens(5).SetCostMicros(50).SetAgentID(aid).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, tot, err := usage.Summary(ctx, c, usage.ByAgent, usage.Filter{OrgID: 1})
	if err != nil || len(rows) != 1 || rows[0].Key != aid || tot.TotalTokens != 5 {
		t.Fatalf("rows=%+v tot=%+v err=%v", rows, tot, err)
	}
}

func TestHourBucketTimezoneBoundary(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 11, 10, 0, 30, 0, 0, loc)
	b := usage.HourBucket(ts, loc)
	want := time.Date(2026, 11, 10, 0, 0, 0, 0, loc).UTC()
	if !b.Equal(want) {
		t.Fatalf("00:30 shanghai bucket %v want %v", b, want)
	}
	prev := time.Date(2026, 11, 9, 23, 30, 0, 0, loc)
	b2 := usage.HourBucket(prev, loc)
	want2 := time.Date(2026, 11, 9, 23, 0, 0, 0, loc).UTC()
	if !b2.Equal(want2) {
		t.Fatalf("23:30 shanghai bucket %v want %v", b2, want2)
	}
}
