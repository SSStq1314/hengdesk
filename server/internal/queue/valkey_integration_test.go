package queue_test

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/SSStq1314/hengdesk/server/internal/queue"
)

// 设置 HENGDESK_VALKEY_IT=1 时对真实 Valkey 运行：入队、重试、延时任务、周期调度。
func TestValkeyAsynqCompat(t *testing.T) {
	if os.Getenv("HENGDESK_VALKEY_IT") != "1" {
		t.Skip("未设置 HENGDESK_VALKEY_IT=1，跳过 Valkey 兼容性测试")
	}
	addr := os.Getenv("HENGDESK_REDIS_ADDR")
	opt := queue.RedisOpt(addr)
	cl := asynq.NewClient(opt)
	defer func() { _ = cl.Close() }()

	var ok, flaky, flakyRuns, delayed, sched atomic.Int32
	var delayedAt atomic.Int64
	mux := asynq.NewServeMux()
	mux.HandleFunc("it:ok", func(context.Context, *asynq.Task) error { ok.Add(1); return nil })
	mux.HandleFunc("it:flaky", func(context.Context, *asynq.Task) error {
		if flakyRuns.Add(1) < 3 {
			return errors.New("模拟失败")
		}
		flaky.Add(1)
		return nil
	})
	mux.HandleFunc("it:delayed", func(context.Context, *asynq.Task) error {
		delayedAt.Store(time.Now().UnixNano())
		delayed.Add(1)
		return nil
	})
	mux.HandleFunc("it:cron", func(context.Context, *asynq.Task) error { sched.Add(1); return nil })

	srv := asynq.NewServer(opt, asynq.Config{
		Concurrency:    4,
		RetryDelayFunc: func(int, error, *asynq.Task) time.Duration { return time.Second },
		Queues:         map[string]int{"it": 1},
	})
	if err := srv.Start(mux); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()

	sc := asynq.NewScheduler(opt, nil)
	if _, err := sc.Register("@every 1s", asynq.NewTask("it:cron", nil), asynq.Queue("it")); err != nil {
		t.Fatal(err)
	}
	if err := sc.Start(); err != nil {
		t.Fatal(err)
	}
	defer sc.Shutdown()

	q := asynq.Queue("it")
	if _, err := cl.Enqueue(asynq.NewTask("it:ok", nil), q); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := cl.Enqueue(asynq.NewTask("it:flaky", nil), q, asynq.MaxRetry(5)); err != nil {
		t.Fatalf("enqueue flaky: %v", err)
	}
	start := time.Now()
	if _, err := cl.Enqueue(asynq.NewTask("it:delayed", nil), q, asynq.ProcessIn(3*time.Second)); err != nil {
		t.Fatalf("enqueue delayed: %v", err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if ok.Load() >= 1 && flaky.Load() >= 1 && delayed.Load() >= 1 && sched.Load() >= 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if ok.Load() < 1 {
		t.Error("普通任务未执行")
	}
	if flaky.Load() < 1 || flakyRuns.Load() < 3 {
		t.Errorf("重试未生效: runs=%d", flakyRuns.Load())
	}
	if delayed.Load() < 1 {
		t.Error("ProcessIn 延时任务未执行")
	} else if d := time.Duration(delayedAt.Load() - start.UnixNano()); d < 3*time.Second {
		t.Errorf("延时任务提前执行: %v", d)
	}
	if sched.Load() < 2 {
		t.Errorf("Scheduler 周期任务执行次数不足: %d", sched.Load())
	}
}
