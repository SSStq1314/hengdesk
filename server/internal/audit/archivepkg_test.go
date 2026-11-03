package audit_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

func chainRecords(t *testing.T, n int) []audit.ArchiveRecord {
	t.Helper()
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	w := audit.EntWriter{C: c}
	for i := 0; i < n; i++ {
		if err := w.Write(ctx, audit.Entry{Action: "t", ActorID: i + 1, ActorEmail: "a@x", Detail: map[string]any{"n": i, "s": "中文"}}); err != nil {
			t.Fatal(err)
		}
	}
	rows := c.AuditLog.Query().Order(ent.Asc("id")).AllX(ctx)
	out := make([]audit.ArchiveRecord, len(rows))
	for i, r := range rows {
		out[i] = audit.RecordFromEnt(r)
	}
	return out
}

func buildPkg(t *testing.T, recs []audit.ArchiveRecord, key ed25519.PrivateKey) []byte {
	t.Helper()
	first, last, chained, err := audit.ChainSegment(recs, "")
	if err != nil {
		t.Fatal(err)
	}
	m := audit.Manifest{ArchiveID: "t1", CreatedAt: time.Now().UTC(), RetentionDays: 365, FirstID: recs[0].ID,
		LastID: recs[len(recs)-1].ID, Count: len(recs), Chained: chained, FirstHash: first, LastHash: last}
	pkg, _, err := audit.BuildPackage(m, recs, key)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestArchivePackageOfflineVerify(t *testing.T) {
	recs := chainRecords(t, 6)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pkg := buildPkg(t, recs, priv)

	rep, err := audit.VerifyPackage(bytes.NewReader(pkg), pub)
	if err != nil || rep.Signature != "verified" || rep.Manifest.Count != 6 || rep.Manifest.LastHash != recs[5].Hash {
		t.Fatalf("verify: %+v %v", rep, err)
	}
	if rep, err := audit.VerifyPackage(bytes.NewReader(pkg), nil); err != nil || rep.Signature != "not_checked" {
		t.Fatalf("no pubkey: %+v %v", rep, err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := audit.VerifyPackage(bytes.NewReader(pkg), other); err == nil {
		t.Fatal("wrong public key must fail")
	}

	// 系统自带 sha256sum 也能校验（文档承诺的用法）。
	if _, err := exec.LookPath("sha256sum"); err == nil {
		dir := t.TempDir()
		f := filepath.Join(dir, "a.tar.gz")
		if err := os.WriteFile(f, pkg, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("tar", "-xzf", f, "-C", dir).CombinedOutput(); err != nil {
			t.Fatalf("tar: %v %s", err, out)
		}
		cmd := exec.Command("sha256sum", "-c", "SHA256SUMS")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sha256sum -c: %v %s", err, out)
		}
	}
}

func TestArchivePackageTamper(t *testing.T) {
	recs := chainRecords(t, 4)
	// 改一条内容但保留原哈希：重算哈希不一致。
	bad := append([]audit.ArchiveRecord(nil), recs...)
	bad[2].Outcome = "forged"
	if _, _, _, err := audit.ChainSegment(bad, ""); err == nil {
		t.Fatal("tampered record must fail chain check")
	}
	// 删掉中间一条：断链。
	gap := append(append([]audit.ArchiveRecord(nil), recs[:1]...), recs[2:]...)
	if _, _, _, err := audit.ChainSegment(gap, ""); err == nil {
		t.Fatal("gap must fail chain check")
	}
	// 包被截断 / 篡改。
	pkg := buildPkg(t, recs, nil)
	if _, err := audit.VerifyPackage(bytes.NewReader(pkg[:len(pkg)/2]), nil); err == nil {
		t.Fatal("truncated package must fail")
	}
	if _, err := audit.VerifyPackage(bytes.NewReader(pkg), ed25519.PublicKey(make([]byte, 32))); err == nil {
		t.Fatal("pubkey given but no signature must fail")
	}
}
