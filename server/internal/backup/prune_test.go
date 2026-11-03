package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPruneKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 7; i++ {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("hengdesk-2026110%dT000000Z-pre-upgrade.dump", i)), []byte("x"), 0o600)
	}
	_ = os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o600)
	removed, err := Prune(dir, 5)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "hengdesk-*.dump"))
	if len(left) != 5 || filepath.Base(left[0]) != "hengdesk-20261103T000000Z-pre-upgrade.dump" {
		t.Fatalf("left=%v", left)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.txt")); err != nil {
		t.Fatal("non-backup file removed")
	}
	if f, err := FreePercent(dir); err != nil || f <= 0 || f > 100 {
		t.Fatalf("free=%v err=%v", f, err)
	}
}
