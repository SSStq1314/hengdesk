package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeDumper struct {
	err  error
	data string
}

func (f fakeDumper) Dump(_ context.Context, _, file string) error {
	if f.err != nil {
		return f.err
	}
	return os.WriteFile(file, []byte(f.data), 0o600)
}

func TestRunOK(t *testing.T) {
	dir := t.TempDir()
	p, err := Run(context.Background(), fakeDumper{data: "PGDMP"}, "postgres://u:p@h/db", dir, "pre-upgrade")
	if err != nil || !strings.HasSuffix(p, "-pre-upgrade.dump") {
		t.Fatalf("%v %s", err, p)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.partial")); len(m) != 0 {
		t.Fatal("partial left")
	}
}

func TestRunFailLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(context.Background(), fakeDumper{err: errors.New("disk full")}, "postgres://h/db", dir, "x"); err == nil {
		t.Fatal("want error")
	}
	if _, err := Run(context.Background(), fakeDumper{}, "postgres://h/db", dir, "x"); err == nil {
		t.Fatal("empty backup must fail")
	}
	if e, _ := os.ReadDir(dir); len(e) != 0 {
		t.Fatalf("leftovers: %v", e)
	}
}

func TestPGEnvHidesPassword(t *testing.T) {
	u, env, err := pgEnv("postgres://hd:s3cret@db:5432/hd?sslmode=disable")
	if err != nil || strings.Contains(u, "s3cret") {
		t.Fatalf("password leaked into args: %s", u)
	}
	found := false
	for _, e := range env {
		found = found || e == "PGPASSWORD=s3cret"
	}
	if !found {
		t.Fatal("PGPASSWORD missing")
	}
}
