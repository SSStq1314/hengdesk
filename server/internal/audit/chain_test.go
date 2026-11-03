package audit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

func TestHashChainVerifyAndTamper(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	w := audit.EntWriter{C: c}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := w.Write(ctx, audit.Entry{Action: "t", ActorID: i, Detail: map[string]any{"n": i, "s": "中文"}}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	res, err := audit.Verify(ctx, c)
	if err != nil || !res.OK || res.Checked != 20 {
		t.Fatalf("verify %+v %v", res, err)
	}
	// 伪造一条：hash 对不上内容。
	last := c.AuditLog.Query().AllX(ctx)
	c.AuditLog.Create().SetAction("forged").SetPrevHash(last[len(last)-1].Hash).SetHash("deadbeef").ExecX(ctx)
	res, _ = audit.Verify(ctx, c)
	if res.OK || res.BrokenAt == 0 {
		t.Fatalf("tamper not detected %+v", res)
	}
}

func TestRecordFailClosed(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	if err := audit.Record(c, audit.Entry{Action: "x"}); !errors.Is(err, audit.ErrRejected) {
		t.Fatalf("no writer: %v", err)
	}
	c.Set("hd.audit.writer", audit.Writer(failWriter{}))
	if err := audit.Record(c, audit.Entry{Action: "x"}); !errors.Is(err, audit.ErrRejected) {
		t.Fatalf("fail writer: %v", err)
	}
}
