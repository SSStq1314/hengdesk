package auditarchive_test

import (
	"context"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auditarchive"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

func TestRetentionCheckAndRemind(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	w := audit.EntWriter{C: c}

	// 空库：没有到期。
	st, err := auditarchive.Check(ctx, c, 365, time.Now())
	if err != nil || st.DueSoon || st.OldestID != 0 {
		t.Fatalf("empty: %+v %v", st, err)
	}
	if err := w.Write(ctx, audit.Entry{Action: "t", ActorID: 1}); err != nil {
		t.Fatal(err)
	}
	// 刚写入、保留 365 天：不提醒。
	sent, st, err := auditarchive.Remind(ctx, c, w, 365, time.Now())
	if err != nil || sent || st.DueSoon || st.DaysLeft < 364 {
		t.Fatalf("fresh: sent=%v %+v %v", sent, st, err)
	}
	// 用短保留期模拟“5 天后到期”：进入 30 天提醒窗口。
	now := time.Now().Add(5 * 24 * time.Hour)
	sent, st, err = auditarchive.Remind(ctx, c, w, 10, now)
	if err != nil || !sent || !st.DueSoon || st.Overdue {
		t.Fatalf("due soon: sent=%v %+v %v", sent, st, err)
	}
	// 7 天内不重复提醒。
	if sent, _, err = auditarchive.Remind(ctx, c, w, 10, now); err != nil || sent {
		t.Fatalf("dedup: sent=%v %v", sent, err)
	}
	alerts := c.AuditLog.Query().Where(auditlog.Action(auth.AlertAction)).AllX(ctx)
	if len(alerts) != 1 || alerts[0].Detail["kind"] != auditarchive.AlertRetentionDue {
		t.Fatalf("alerts %+v", alerts)
	}
	// 已超期。
	st, _ = auditarchive.Check(ctx, c, 10, time.Now().Add(11*24*time.Hour))
	if !st.Overdue || st.DaysLeft >= 0 {
		t.Fatalf("overdue %+v", st)
	}
	// 链仍完整（提醒本身也是审计记录）。
	if res, err := audit.Verify(ctx, c); err != nil || !res.OK {
		t.Fatalf("verify %+v %v", res, err)
	}
}
