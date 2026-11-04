package kb

import (
	"context"
	"os"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/store"
)

// TestPGLoadFilesManyIDs 可见文件超过 65535 个时不能因扩展协议参数上限而失败（PR4 复测发现）。
func TestPGLoadFilesManyIDs(t *testing.T) {
	if os.Getenv("HENGDESK_PG_IT") != "1" {
		t.Skip("set HENGDESK_PG_IT=1")
	}
	c, db, err := store.Open(os.Getenv("HENGDESK_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if _, err := store.ApplyMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	ids := make([]int, 70000)
	for i := range ids {
		ids[i] = 1_000_000_000 + i
	}
	fs, err := loadFiles(ctx, c, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Fatalf("want 0 files, got %d", len(fs))
	}
}
