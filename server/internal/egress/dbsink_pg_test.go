package egress_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/egress"
	"github.com/SSStq1314/hengdesk/server/internal/ent/egresslog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/egressrule"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

// 需要 PostgreSQL（含 append-only 触发器）：HENGDESK_PG_IT=1 HENGDESK_TEST_DATABASE_URL=...（会清空该库的 public schema）。
func TestPGEgressAllowLogComplete(t *testing.T) {
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

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(up.Close)

	c.EgressRule.Create().SetKind(egressrule.KindIP).SetValue("127.0.0.1").SetPorts("*").SetPrivate(true).SetNote("test").ExecX(ctx)

	var logs bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&logs, nil))
	cl, _ := egress.New(c, audit.EntWriter{C: c}, lg)

	resp, err := cl.HTTP().Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"ok"`)) {
		t.Fatalf("upstream %d %s", resp.StatusCode, body)
	}

	rows := c.EgressLog.Query().Where(egresslog.DecisionEQ(egresslog.DecisionAllow)).AllX(ctx)
	if len(rows) != 1 {
		t.Fatalf("放行记录数=%d %+v", len(rows), rows)
	}
	row := rows[0]
	if row.Status != http.StatusOK {
		t.Fatalf("status=%d want 200", row.Status)
	}
	if row.IP != "127.0.0.1" {
		t.Fatalf("ip=%q", row.IP)
	}
	if row.DurationMs <= 0 {
		t.Fatalf("duration_ms=%d，应记录耗时", row.DurationMs)
	}
	if strings.Contains(logs.String(), "append-only") {
		t.Fatalf("服务日志不应出现 append-only 报错：%s", logs.String())
	}

	_, err = db.ExecContext(ctx, `UPDATE egress_logs SET status = 201`)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("append-only 触发器应拒绝 UPDATE：%v", err)
	}
}
