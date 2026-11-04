package api_test

// 压测（非功能指标）：首 token 时延与 200 并发会话，全链路（登录会话 → 授权 → 预算 → 审计 → 网关 → 出站控制 → mock 上游 → SSE）。
// 只用 mock 模型，不访问真实上游。默认跳过：HENGDESK_LOADTEST=1 go test ./internal/api -run TestLoadChat -v
// 可调：HENGDESK_LOADTEST_N（并发会话数，默认 200）、HENGDESK_LOADTEST_UPSTREAM_MS（mock 上游首 token 延迟，默认 300）。

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
)

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	i := int(float64(len(d))*p+0.5) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(d) {
		i = len(d) - 1
	}
	return d[i]
}

func TestLoadChat(t *testing.T) {
	if os.Getenv("HENGDESK_LOADTEST") != "1" {
		t.Skip("set HENGDESK_LOADTEST=1")
	}
	n := envInt("HENGDESK_LOADTEST_N", 200)
	upMs := envInt("HENGDESK_LOADTEST_UPSTREAM_MS", 300)
	g := newGW(t, false)
	g.m.Delay = time.Duration(upMs) * time.Millisecond
	g.m.TokenDelay = 5 * time.Millisecond
	srv := httptest.NewServer(g.h)
	defer srv.Close()
	ids := make([]int, n)
	for i := range ids {
		ids[i] = newConv(t, g.admin, `{}`)
	}
	hc := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: n, MaxConnsPerHost: 0}, Timeout: 2 * time.Minute}
	var (
		mu    sync.Mutex
		ttft  []time.Duration
		total []time.Duration
		fails []string
		wg    sync.WaitGroup
	)
	start := make(chan struct{})
	for _, id := range ids {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, fmt.Sprintf("%s/api/v1/conversations/%d/messages", srv.URL, id),
				strings.NewReader(`{"content":"压测：请简单回答","model":"ds-flash"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(auth.CSRFHeader, g.admin.csrf)
			for _, ck := range g.admin.cks {
				req.AddCookie(ck)
			}
			t0 := time.Now()
			resp, err := hc.Do(req)
			if err != nil {
				mu.Lock()
				fails = append(fails, err.Error())
				mu.Unlock()
				return
			}
			defer func() { _ = resp.Body.Close() }()
			var first time.Duration
			done := false
			sc := bufio.NewScanner(resp.Body)
			for sc.Scan() {
				l := sc.Text()
				if first == 0 && l == "event: token" {
					first = time.Since(t0)
				}
				if l == "event: done" {
					done = true
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if resp.StatusCode != 200 || !done || first == 0 {
				fails = append(fails, fmt.Sprintf("status=%d done=%v", resp.StatusCode, done))
				return
			}
			ttft = append(ttft, first)
			total = append(total, time.Since(t0))
		}(id)
	}
	t0 := time.Now()
	close(start)
	wg.Wait()
	wall := time.Since(t0)
	sort.Slice(ttft, func(i, j int) bool { return ttft[i] < ttft[j] })
	sort.Slice(total, func(i, j int) bool { return total[i] < total[j] })
	ms := func(d time.Duration) string { return fmt.Sprintf("%.0fms", float64(d)/float64(time.Millisecond)) }
	t.Logf("LOADTEST sessions=%d upstream_first_token=%dms ok=%d fail=%d wall=%s", n, upMs, len(ttft), len(fails), ms(wall))
	t.Logf("LOADTEST ttft p50=%s p95=%s p99=%s max=%s", ms(pct(ttft, .5)), ms(pct(ttft, .95)), ms(pct(ttft, .99)), ms(pct(ttft, 1)))
	t.Logf("LOADTEST overhead(ttft-upstream) p50=%s p95=%s", ms(pct(ttft, .5)-g.m.Delay), ms(pct(ttft, .95)-g.m.Delay))
	t.Logf("LOADTEST total p50=%s p95=%s p99=%s", ms(pct(total, .5)), ms(pct(total, .95)), ms(pct(total, .99)))
	if len(fails) > 0 {
		t.Fatalf("%d 个会话失败，例如：%s", len(fails), fails[0])
	}
}
