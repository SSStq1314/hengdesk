package kb

import (
	"context"
	"os"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/gateway/mockupstream"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

// 需要带 pgvector 的 PostgreSQL：HENGDESK_PG_IT=1 HENGDESK_TEST_DATABASE_URL=...（会清空该库的 public schema）。
func TestPGStore(t *testing.T) {
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
	s := PGStore{DB: db}
	mk := func(file, n int) []Chunk {
		out := make([]Chunk, n)
		for i := range out {
			out[i] = Chunk{OrgID: 1, KBID: 7, FileID: file, Piece: Piece{Seq: i, Content: "出差住宿标准第" + string(rune('a'+i%26)), Heading: "差旅"},
				Embedding: mockupstream.HashEmbed("出差住宿", Dim)}
		}
		return out
	}
	if err := s.Replace(ctx, 1, mk(1, 250)); err != nil { // 跨多个批次
		t.Fatal(err)
	}
	if err := s.Replace(ctx, 2, mk(2, 3)); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(ctx, 1, mk(1, 5)); err != nil { // 替换而非追加
		t.Fatal(err)
	}
	if n, _ := s.CountFile(ctx, 1); n != 5 {
		t.Fatalf("file1 %d", n)
	}
	var hit int
	if err := db.QueryRow(`SELECT count(*) FROM kb_chunks WHERE tsv @@ to_tsquery('simple', $1)`, TSQuery("住宿")).Scan(&hit); err != nil || hit != 8 {
		t.Fatalf("tsv hits %d %v", hit, err)
	}
	if err := s.DeleteFile(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountFile(ctx, 2); n != 0 {
		t.Fatal("delete file")
	}
	if err := s.DeleteKB(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountFile(ctx, 1); n != 0 {
		t.Fatal("delete kb")
	}
	bad := mk(3, 1)
	bad[0].Embedding = bad[0].Embedding[:10]
	if err := s.Replace(ctx, 3, bad); err == nil {
		t.Fatal("维度不符应失败")
	}
}
