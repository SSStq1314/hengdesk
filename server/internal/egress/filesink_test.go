package egress_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/egress"
)

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// JSONLSink：放行与拒绝的请求都写日志；日志写不进去时不发请求（未声明 fail-closed 的调用方）。
func TestJSONLSink(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	var buf bytes.Buffer
	cl := (&egress.Client{Policy: egress.MustStatic("cidr 127.0.0.0/8 *"), Sink: &egress.JSONLSink{W: &buf}}).HTTP()
	ctx := egress.WithCaller(context.Background(), egress.Caller{Module: "cli.test"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/x", nil)
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "https://denied.example.com/", nil)
	if resp, err := cl.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("应拒绝")
	}
	out := buf.String()
	if strings.Count(out, "\n") < 3 || !strings.Contains(out, `"Module":"cli.test"`) || !strings.Contains(out, "denied.example.com") {
		t.Fatalf("log: %s", out)
	}
	bad := (&egress.Client{Policy: egress.MustStatic("cidr 127.0.0.0/8 *"), Sink: &egress.JSONLSink{W: failWriter{}}}).HTTP()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/y", nil)
	if resp, err := bad.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("日志写不进去应拒绝")
	}
	if hits != 1 {
		t.Fatalf("hits %d", hits)
	}
}
