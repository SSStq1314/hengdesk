package usage_test

import (
	"context"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
	"github.com/SSStq1314/hengdesk/server/internal/usage"
)

func TestCost(t *testing.T) {
	// DeepSeek 风格：输入 2 元/M、缓存 0.5 元/M、输出 8 元/M。
	p := usage.Price{InputMicros: 2_000_000, CachedMicros: 500_000, OutputMicros: 8_000_000}
	u := gateway.Usage{PromptTokens: 411, CachedTokens: 256, CompletionTokens: 145, ReasoningTokens: 4}
	// (155*2 + 256*0.5 + 145*8) 元/M = 1598 微元
	if got := usage.Cost(u, p); got != 1598 {
		t.Fatalf("cost=%d", got)
	}
	if got := usage.Cost(gateway.Usage{PromptTokens: 1}, usage.Price{InputMicros: 400_000}); got != 0 {
		t.Fatalf("rounding: %d", got)
	}
	if got := usage.Cost(gateway.Usage{PromptTokens: 2}, usage.Price{InputMicros: 400_000}); got != 1 {
		t.Fatalf("rounding up: %d", got)
	}
	if got := usage.Cost(gateway.Usage{PromptTokens: 10, CachedTokens: 99}, usage.Price{InputMicros: 1e6, CachedMicros: 0}); got != 0 {
		t.Fatalf("cached clamp: %d", got)
	}
}

func TestWriteAndSummary(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	pv := c.ModelProvider.Create().SetName("p").SetBaseURL("http://x").SaveX(ctx)
	m1 := c.Model.Create().SetName("a").SetProviderID(pv.ID).SetUpstreamModel("a").
		SetPriceInputMicros(1_000_000).SetPriceOutputMicros(2_000_000).SaveX(ctx)
	m2 := c.Model.Create().SetName("b").SetProviderID(pv.ID).SetUpstreamModel("b").SaveX(ctx)
	d1, d2 := 10, 20
	w := func(user int, dept *int, m string, u gateway.Usage, errCode string) {
		mm := m1
		if m == "b" {
			mm = m2
		}
		if _, err := usage.Write(ctx, c, usage.Record{OrgID: 1, UserID: user, DeptID: dept, Model: mm, ProviderID: pv.ID,
			Usage: u, ErrorCode: errCode, Latency: time.Second}); err != nil {
			t.Fatal(err)
		}
	}
	w(1, &d1, "a", gateway.Usage{PromptTokens: 1000, CompletionTokens: 1000}, "")              // 3000 微元
	w(1, &d1, "a", gateway.Usage{PromptTokens: 500, CompletionTokens: 0, Estimated: true}, "") // 500 微元（估算）
	w(2, &d2, "b", gateway.Usage{PromptTokens: 10, CompletionTokens: 10}, "")                  // 0
	w(3, nil, "a", gateway.Usage{PromptTokens: 0, CompletionTokens: 0, Estimated: true}, "upstream_timeout")
	if _, err := usage.Write(ctx, c, usage.Record{OrgID: 2, UserID: 9, Model: m1, Usage: gateway.Usage{PromptTokens: 1e6}}); err != nil {
		t.Fatal(err)
	}

	rows, tot, err := usage.Summary(ctx, c, usage.ByUser, usage.Filter{OrgID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if tot.Calls != 4 || tot.CostMicros != 3500 || tot.EstimatedCalls != 2 || tot.EstimatedCostMicros != 500 || tot.ErrorCalls != 1 || tot.TotalTokens != 2520 {
		t.Fatalf("totals %+v", tot)
	}
	if len(rows) != 3 || rows[0].Key != 1 || rows[0].CostMicros != 3500 || rows[0].EstimatedTokens != 500 {
		t.Fatalf("rows %+v", rows)
	}
	rows, _, err = usage.Summary(ctx, c, usage.ByDept, usage.Filter{OrgID: 1})
	if err != nil || len(rows) != 3 {
		t.Fatalf("dept %v %+v", err, rows)
	}
	rows, _, err = usage.Summary(ctx, c, usage.ByModel, usage.Filter{OrgID: 1, DeptIDs: []int{d1}})
	if err != nil || len(rows) != 1 || rows[0].Key != m1.ID || rows[0].Calls != 2 {
		t.Fatalf("model %v %+v", err, rows)
	}
	_, tot, err = usage.Summary(ctx, c, usage.ByNone, usage.Filter{OrgID: 1, From: time.Now().Add(time.Hour)})
	if err != nil || tot.Calls != 0 {
		t.Fatalf("from filter %v %+v", err, tot)
	}
	if _, _, err := usage.Summary(ctx, c, "bogus", usage.Filter{OrgID: 1}); err == nil {
		t.Fatal("bad group")
	}
}
