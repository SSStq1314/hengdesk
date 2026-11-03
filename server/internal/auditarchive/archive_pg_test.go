package auditarchive

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

// 需要 PostgreSQL：HENGDESK_PG_IT=1 HENGDESK_TEST_DATABASE_URL=...（会清空该库的 public schema）。
func pgClient(t *testing.T) (*ent.Client, *sql.DB) {
	t.Helper()
	if os.Getenv("HENGDESK_PG_IT") != "1" {
		t.Skip("set HENGDESK_PG_IT=1")
	}
	c, db, err := store.Open(os.Getenv("HENGDESK_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return c, db
}

type memStore struct {
	mu      sync.Mutex
	m       map[string][]byte
	failPut bool
}

func (s *memStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	if s.failPut {
		return errors.New("s3 down")
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = b
	return nil
}

func (s *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// seed 写 n 条旧记录，再写 m 条新记录；返回让前 n 条恰好过期的 Now。
func seed(t *testing.T, c *ent.Client, n, m int) func() time.Time {
	t.Helper()
	ctx := context.Background()
	w := audit.EntWriter{C: c}
	for i := 0; i < n; i++ {
		if err := w.Write(ctx, audit.Entry{Action: "old", ActorID: 1, Detail: map[string]any{"i": i}}); err != nil {
			t.Fatal(err)
		}
	}
	all := c.AuditLog.Query().Order(ent.Asc("id")).AllX(ctx)
	edge := all[len(all)-1].Ts
	time.Sleep(20 * time.Millisecond)
	for i := 0; i < m; i++ {
		if err := w.Write(ctx, audit.Entry{Action: "new", ActorID: 1, Detail: map[string]any{"i": i}}); err != nil {
			t.Fatal(err)
		}
	}
	at := edge.Add(365*24*time.Hour + 5*time.Millisecond)
	return func() time.Time { return at }
}

// original 只数业务记录（检查点等归档自身写入的行不算）。
func original(t *testing.T, c *ent.Client) int {
	return c.AuditLog.Query().Where(auditlog.ActionIn("old", "new")).CountX(context.Background())
}

func mustVerify(t *testing.T, c *ent.Client) audit.VerifyResult {
	t.Helper()
	res, err := audit.Verify(context.Background(), c)
	if err != nil || !res.OK {
		t.Fatalf("chain verify: %+v %v", res, err)
	}
	return res
}

func TestPGArchiveFailClosed(t *testing.T) {
	c, db := pgClient(t)
	ctx := context.Background()
	now := seed(t, c, 5, 3)
	before := original(t, c)
	actor := audit.Entry{ActorID: 1, ActorEmail: "auditor@x"}

	for _, tc := range []struct {
		name      string
		failPut   bool
		failAfter string
	}{{"s3-put-fails", true, ""}, {"after-upload", false, "uploaded"}, {"after-checkpoint", false, "checkpointed"}} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Archiver{C: c, DB: db, W: audit.EntWriter{C: c}, S: &memStore{m: map[string][]byte{}, failPut: tc.failPut},
				RetentionDays: 365, Now: now, failAfter: tc.failAfter}
			if _, err := a.Run(ctx, actor); err == nil {
				t.Fatal("expected failure")
			}
			// 原始记录一条不少（检查点行可能多出来，属于正常追加）。
			if got := original(t, c); got != before {
				t.Fatalf("data lost: %d != %d", got, before)
			}
			mustVerify(t, c)
		})
	}
}

func TestPGArchiveSuccessAndOfflineVerify(t *testing.T) {
	c, db := pgClient(t)
	ctx := context.Background()
	now := seed(t, c, 5, 3)
	s := &memStore{m: map[string][]byte{}}
	a := &Archiver{C: c, DB: db, W: audit.EntWriter{C: c}, S: s, RetentionDays: 365, Now: now}
	res, err := a.Run(ctx, audit.Entry{ActorID: 1, ActorEmail: "auditor@x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 5 || res.Manifest.Count != 5 {
		t.Fatalf("result %+v", res)
	}
	if got := original(t, c); got != 3 {
		t.Fatalf("remaining %d", got)
	}
	// 检查点之后整链仍可验证，且锚定到检查点。
	v := mustVerify(t, c)
	if v.AnchorID != res.CheckpointID {
		t.Fatalf("anchor %d != checkpoint %d", v.AnchorID, res.CheckpointID)
	}
	// 对象存储里的包可以离线校验。
	rep, err := audit.VerifyPackage(bytes.NewReader(s.m[res.ObjectKey]), nil)
	if err != nil || rep.Manifest.LastHash != res.Manifest.LastHash || rep.Manifest.Count != 5 {
		t.Fatalf("offline verify %+v %v", rep, err)
	}
	// 没有到期记录时不做任何事。
	if _, err := a.Run(ctx, audit.Entry{ActorID: 1}); !errors.Is(err, ErrNothingToArchive) {
		t.Fatalf("second run: %v", err)
	}
	// 第二轮：新记录也过期后再归档，链仍可验证。
	a.Now = func() time.Time { return time.Now().Add(400 * 24 * time.Hour) }
	if _, err := a.Run(ctx, audit.Entry{ActorID: 1, ActorEmail: "auditor@x"}); err != nil {
		t.Fatal(err)
	}
	mustVerify(t, c)
}

func TestPGDeleteRequiresCheckpoint(t *testing.T) {
	c, db := pgClient(t)
	ctx := context.Background()
	seed(t, c, 3, 0)
	if _, err := db.ExecContext(ctx, `DELETE FROM audit_logs`); err == nil {
		t.Fatal("delete without checkpoint must be rejected")
	}
	// 会话变量指向普通行（非检查点）同样拒绝。
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('hengdesk.audit_archive_checkpoint', '3', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM audit_logs WHERE id = 1`); err == nil {
		t.Fatal("delete with non-checkpoint row must be rejected")
	}
	if _, err := db.ExecContext(ctx, `UPDATE audit_logs SET outcome = 'x'`); err == nil {
		t.Fatal("update must be rejected")
	}
}
