package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/SSStq1314/hengdesk/server/internal/store"
)

// 需要 PostgreSQL 17 + pgvector：HENGDESK_PG_IT=1 HENGDESK_TEST_DATABASE_URL=...（会清空该库的 public schema）。
func pgDB(t *testing.T) *sql.DB {
	t.Helper()
	if os.Getenv("HENGDESK_PG_IT") != "1" {
		t.Skip("set HENGDESK_PG_IT=1")
	}
	db, err := sql.Open("pgx", os.Getenv("HENGDESK_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrationsForwardOnly(t *testing.T) {
	db := pgDB(t)
	ctx := context.Background()
	st, err := store.Status(ctx, db)
	if err != nil || !st.Fresh || len(st.Pending) == 0 {
		t.Fatalf("fresh status: %+v %v", st, err)
	}
	applied, err := store.ApplyMigrations(ctx, db)
	if err != nil || len(applied) != len(st.Pending) {
		t.Fatalf("apply: %v %v", applied, err)
	}
	again, err := store.ApplyMigrations(ctx, db)
	if err != nil || len(again) != 0 {
		t.Fatalf("second apply must be no-op: %v %v", again, err)
	}
	// 审计表只追加（数据库触发器）。
	if _, err := db.Exec(`INSERT INTO audit_logs (ts, action) VALUES (now(), 'it')`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE audit_logs SET outcome='x'`, `DELETE FROM audit_logs`, `TRUNCATE audit_logs`} {
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("%s must fail", q)
		}
	}
	// 数据库比程序新（降级运行）必须拒绝。
	if _, err := db.Exec(`INSERT INTO hengdesk_schema_revisions(version,name,hash) VALUES ('99990101000000','future.sql','x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Status(ctx, db); err == nil {
		t.Fatal("downgrade must be rejected")
	}
	if _, err := db.Exec(`DELETE FROM hengdesk_schema_revisions WHERE version='99990101000000'`); err != nil {
		t.Fatal(err)
	}
	// 已执行文件被篡改（hash 不符）必须拒绝。
	if _, err := db.Exec(`UPDATE hengdesk_schema_revisions SET hash='tampered' WHERE version=(SELECT min(version) FROM hengdesk_schema_revisions)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Status(ctx, db); err == nil {
		t.Fatal("tampered migration must be rejected")
	}
}

func TestLegacySchemaRejected(t *testing.T) {
	db := pgDB(t)
	if _, err := db.Exec(`CREATE TABLE users (id int)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Status(context.Background(), db); !errors.Is(err, store.ErrLegacySchema) {
		t.Fatalf("want legacy error, got %v", err)
	}
}
