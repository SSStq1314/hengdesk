package budget

import (
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/budget"
)

func TestPeriodWindow(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 12, 31, 23, 59, 0, 0, loc)
	w := PeriodWindow(budget.PeriodMonth, now, loc)
	if w.Key != "20261201" || !w.End.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("month %+v", w)
	}
	w = PeriodWindow(budget.PeriodDay, now.Add(2*time.Minute), loc)
	if w.Key != "20270101" {
		t.Fatalf("day %+v", w)
	}
	// UTC 时间按配置时区切周期
	w = PeriodWindow(budget.PeriodDay, time.Date(2026, 12, 31, 17, 0, 0, 0, time.UTC), loc)
	if w.Key != "20270101" {
		t.Fatalf("tz %+v", w)
	}
}

func TestLevel(t *testing.T) {
	b := &ent.Budget{LimitValue: 1000, ThresholdPct: 80}
	for used, want := range map[int64]int{0: 0, 799: 0, 800: 80, 999: 80, 1000: 100, 1500: 100} {
		if got := Level(b, used); got != want {
			t.Errorf("%d → %d want %d", used, got, want)
		}
	}
}
