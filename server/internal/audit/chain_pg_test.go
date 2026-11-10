package audit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

// 需要 PostgreSQL：HENGDESK_PG_IT=1 HENGDESK_TEST_DATABASE_URL=...
// 父进程会清空该库 public schema 并执行迁移；子进程只打开独立连接池，不清空。

type childResult struct {
	OK    int    `json:"ok"`
	Fail  int    `json:"fail"`
	First string `json:"first,omitempty"`
}

func pgAuditParent(t *testing.T) (*ent.Client, string) {
	t.Helper()
	if os.Getenv("HENGDESK_AUDIT_CHAIN_CHILD") != "" {
		t.Skip("child process")
	}
	if os.Getenv("HENGDESK_PG_IT") != "1" {
		t.Skip("set HENGDESK_PG_IT=1")
	}
	dsn := os.Getenv("HENGDESK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("HENGDESK_TEST_DATABASE_URL required")
	}
	c, db, err := store.Open(dsn)
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
	return c, dsn
}

// TestPGTwoProcessConcurrentChainWrite 模拟 server 与 worker：两个独立进程、各 100 个 goroutine
// 在同一时刻突发写入（合计 200 并发），每 goroutine 写 10 条，共 2000 条。
// 断言 0 失败且全链校验通过。一半写入走调用方外层事务，覆盖 EntWriter{C: tx.Client()} 路径。
func TestPGTwoProcessConcurrentChainWrite(t *testing.T) {
	c, dsn := pgAuditParent(t)
	dir := t.TempDir()
	start := filepath.Join(dir, "start")
	const processes, goroutines, perG = 2, 100, 10
	want := processes * goroutines * perG

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmds := make([]*exec.Cmd, processes)
	for i := 0; i < processes; i++ {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPGAuditChainChild$", "-test.count=1")
		cmd.Env = append(os.Environ(),
			"HENGDESK_PG_IT=1",
			"HENGDESK_TEST_DATABASE_URL="+dsn,
			"HENGDESK_AUDIT_CHAIN_CHILD=1",
			"HENGDESK_AUDIT_CHAIN_ID="+fmt.Sprintf("%d", i),
			fmt.Sprintf("HENGDESK_AUDIT_CHAIN_G=%d", goroutines),
			fmt.Sprintf("HENGDESK_AUDIT_CHAIN_N=%d", perG),
			"HENGDESK_AUDIT_CHAIN_READY="+filepath.Join(dir, fmt.Sprintf("ready%d", i)),
			"HENGDESK_AUDIT_CHAIN_START="+start,
			"HENGDESK_AUDIT_CHAIN_OUT="+filepath.Join(dir, fmt.Sprintf("out%d.json", i)),
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = cmd
	}

	deadline := time.Now().Add(30 * time.Second)
	for i := 0; i < processes; i++ {
		ready := filepath.Join(dir, fmt.Sprintf("ready%d", i))
		for {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("child %d not ready", i)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if err := os.WriteFile(start, []byte("go\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now()
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child %d: %v", i, err)
		}
	}
	elapsed := time.Since(t0)

	ok, fail := 0, 0
	for i := 0; i < processes; i++ {
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("out%d.json", i)))
		if err != nil {
			t.Fatalf("read child %d result: %v", i, err)
		}
		var r childResult
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("child %d json: %v %s", i, err, b)
		}
		if r.Fail != 0 {
			t.Errorf("child %d failures=%d first=%s", i, r.Fail, r.First)
		}
		ok += r.OK
		fail += r.Fail
	}
	res, err := audit.Verify(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("processes=%d goroutines_per_process=%d writes_per_goroutine=%d total=%d ok=%d fail=%d verify_ok=%v checked=%d elapsed=%s",
		processes, goroutines, perG, want, ok, fail, res.OK, res.Checked, elapsed)
	if fail != 0 || ok != want {
		t.Fatalf("want %d successful writes and 0 failures, got ok=%d fail=%d", want, ok, fail)
	}
	if !res.OK || res.Checked != want {
		t.Fatalf("chain verify %+v, want checked=%d", res, want)
	}
}

// TestPGAuditChainChild 由 TestPGTwoProcessConcurrentChainWrite 拉起，不要单独跑。
func TestPGAuditChainChild(t *testing.T) {
	if os.Getenv("HENGDESK_AUDIT_CHAIN_CHILD") == "" {
		t.Skip("parent-only helper")
	}
	dsn := os.Getenv("HENGDESK_TEST_DATABASE_URL")
	c, db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(32)

	g, errG := strconv.Atoi(os.Getenv("HENGDESK_AUDIT_CHAIN_G"))
	n, errN := strconv.Atoi(os.Getenv("HENGDESK_AUDIT_CHAIN_N"))
	if errG != nil || errN != nil || g <= 0 || n <= 0 {
		t.Fatalf("bad G/N %s %s", os.Getenv("HENGDESK_AUDIT_CHAIN_G"), os.Getenv("HENGDESK_AUDIT_CHAIN_N"))
	}
	id := os.Getenv("HENGDESK_AUDIT_CHAIN_ID")
	if err := os.WriteFile(os.Getenv("HENGDESK_AUDIT_CHAIN_READY"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	start := os.Getenv("HENGDESK_AUDIT_CHAIN_START")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(start); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("start signal timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}

	var ok, fail atomic.Int64
	var first atomic.Value
	var wg sync.WaitGroup
	var barrier sync.WaitGroup
	barrier.Add(g)
	for i := 0; i < g; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			barrier.Done()
			barrier.Wait()
			w := audit.EntWriter{C: c}
			ctx := context.Background()
			for k := 0; k < n; k++ {
				e := audit.Entry{
					Action: "chain.bench", ActorEmail: "it@x", ActorID: i + 1,
					ResourceType: "test", ResourceID: id,
					// D5：并发写入同时带 struct / 嵌套 / 中文 detail。
					Detail: map[string]any{"p": id, "g": i, "k": k, "usage": usageLike{Prompt: k, Completion: i, Total: i + k, Zeta: "中文"}},
				}
				var err error
				if i%2 == 0 {
					tx, txErr := c.Tx(ctx)
					if txErr != nil {
						err = txErr
					} else {
						err = (audit.EntWriter{C: tx.Client()}).Write(ctx, e)
						if err != nil {
							_ = tx.Rollback()
						} else {
							err = tx.Commit()
						}
					}
				} else {
					err = w.Write(ctx, e)
				}
				if err != nil {
					fail.Add(1)
					first.CompareAndSwap(nil, err.Error())
					continue
				}
				ok.Add(1)
			}
		}(i)
	}
	wg.Wait()
	r := childResult{OK: int(ok.Load()), Fail: int(fail.Load())}
	if v, _ := first.Load().(string); v != "" {
		r.First = v
	}
	b, _ := json.Marshal(r)
	if err := os.WriteFile(os.Getenv("HENGDESK_AUDIT_CHAIN_OUT"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPGChainVerifyStructAndSpecialDetails D5 回归（PostgreSQL jsonb 往返）。
func TestPGChainVerifyStructAndSpecialDetails(t *testing.T) {
	c, _ := pgAuditParent(t)
	n := writeD5(t, c)
	res, err := audit.Verify(context.Background(), c)
	if err != nil || !res.OK || res.Checked != n {
		t.Fatalf("verify %+v %v", res, err)
	}
}

func TestPGWriteInsideCallerTx(t *testing.T) {
	c, _ := pgAuditParent(t)
	ctx := context.Background()
	tx, err := c.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w := audit.EntWriter{C: tx.Client()}
	if err := w.Write(ctx, audit.Entry{Action: "outer", ActorEmail: "a@x"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	res, err := audit.Verify(ctx, c)
	if err != nil || !res.OK || res.Checked != 1 {
		t.Fatalf("verify %+v %v", res, err)
	}
}
