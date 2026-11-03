package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

func TestRotateMasterFile(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	if err := os.WriteFile(path, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldK, err := gateway.LoadKeyring(path, "")
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := oldK.Seal("ds", "sk-x")
	c.ModelProvider.Create().SetName("ds").SetBaseURL("http://x").SetAPIKeyEnc(enc).ExecX(ctx)

	res, err := rotateMaster(ctx, c, path, oldK, time.Date(2026, 11, 3, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	newK, err := gateway.LoadKeyring(path, "")
	if err != nil || newK.Fingerprint() != res.NewFingerprint || newK.Fingerprint() == oldK.Fingerprint() {
		t.Fatalf("new key file: %v", err)
	}
	if err := gateway.SelfCheck(ctx, c, newK, true); err != nil {
		t.Fatal(err)
	}
	bak, _ := os.ReadFile(res.Backup)
	if strings.TrimSpace(string(bak)) != strings.Repeat("ab", 32) || !strings.HasSuffix(res.Backup, ".bak.20261103T010203Z") {
		t.Fatalf("backup %s", res.Backup)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("perm %v", st.Mode())
	}
	if _, err := os.Stat(path + ".new"); !os.IsNotExist(err) {
		t.Fatal(".new 应已替换")
	}
	if n := c.AuditLog.Query().Where(auditlog.Action("model.master_key.rotate")).CountX(ctx); n != 1 {
		t.Fatalf("audit %d", n)
	}
	// 用旧密钥再轮换一次：解不开 → 失败、文件不变、无 .new
	before, _ := os.ReadFile(path)
	if _, err := rotateMaster(ctx, c, path, oldK, time.Now()); err == nil {
		t.Fatal("stale old key should fail")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("失败时不应改动主密钥文件")
	}
	if _, err := os.Stat(path + ".new"); !os.IsNotExist(err) {
		t.Fatal("失败时应删除 .new")
	}
}
