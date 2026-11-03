package storetest

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite" // 纯 Go sqlite，仅用于单测

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

var testDBSeq atomic.Int64

// NewTestClient 返回一个迁移好的内存 SQLite ent 客户端（仅测试用）。
func NewTestClient(t testing.TB) *ent.Client {
	t.Helper()
	dsn := fmt.Sprintf("file:hd%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", testDBSeq.Add(1))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	c := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	if err := store.CreateSchemaForTest(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
