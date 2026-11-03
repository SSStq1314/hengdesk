package audit_test

// D-41 第 1 条：trace_id 以服务端生成为准；只接受受信代理传入的 traceparent，单独存 upstream_trace_id 仅作关联。

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

const tpTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
const tpHeader = "00-" + tpTrace + "-00f067aa0ba902b7-01"

func TestParseTraceparent(t *testing.T) {
	for _, c := range []struct {
		in string
		ok bool
	}{
		{tpHeader, true},
		{"  00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01 ", true},
		{"01-" + tpTrace + "-00f067aa0ba902b7-01-extra", true}, // 未来版本允许追加字段
		{"00-" + tpTrace + "-00f067aa0ba902b7-01-extra", false},
		{"ff-" + tpTrace + "-00f067aa0ba902b7-01", false},
		{"00-00000000000000000000000000000000-00f067aa0ba902b7-01", false},
		{"00-" + tpTrace + "-0000000000000000-01", false},
		{"00-" + tpTrace + "-00f067aa0ba902b7", false},
		{"00-xyz-00f067aa0ba902b7-01", false},
		{"", false},
	} {
		got, ok := audit.ParseTraceparent(c.in)
		if ok != c.ok || (ok && got != tpTrace) {
			t.Errorf("%q => %q %v", c.in, got, ok)
		}
	}
}

func runTrace(t *testing.T, trusted []*net.IPNet, remote, tp string) (string, string) {
	t.Helper()
	e := echo.New()
	var gotTrace, gotUp string
	e.GET("/", func(c echo.Context) error {
		gotTrace = audit.TraceFrom(c.Request().Context())
		gotUp = audit.UpstreamTraceFrom(c.Request().Context())
		return c.NoContent(204)
	}, audit.Trace(trusted))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remote + ":1234"
	if tp != "" {
		req.Header.Set(audit.HeaderTraceparent, tp)
	}
	req.Header.Set("X-Forwarded-For", "10.9.9.9") // 伪造的 XFF 不影响受信判断（只看 TCP 对端）
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if h := rec.Header().Get(audit.HeaderTraceID); h != gotTrace || !audit.ValidTraceID(gotTrace) {
		t.Fatalf("响应头 trace %q vs %q", h, gotTrace)
	}
	if gotTrace == tpTrace {
		t.Fatal("trace_id 必须由服务端生成，不能采用外部值")
	}
	return gotTrace, gotUp
}

func TestTraceTrustedProxies(t *testing.T) {
	_, proxy, _ := net.ParseCIDR("10.0.0.0/8")
	// 默认空列表：一律忽略
	if _, up := runTrace(t, nil, "10.1.2.3", tpHeader); up != "" {
		t.Fatalf("默认不应接受 traceparent：%q", up)
	}
	// 不在列表内：忽略
	if _, up := runTrace(t, []*net.IPNet{proxy}, "192.0.2.9", tpHeader); up != "" {
		t.Fatalf("非受信来源不应接受：%q", up)
	}
	// 受信：单独记录
	if _, up := runTrace(t, []*net.IPNet{proxy}, "10.1.2.3", tpHeader); up != tpTrace {
		t.Fatalf("受信来源应接受：%q", up)
	}
	// 受信但格式非法：忽略
	if _, up := runTrace(t, []*net.IPNet{proxy}, "10.1.2.3", "garbage"); up != "" {
		t.Fatalf("非法 traceparent 应忽略：%q", up)
	}
}

func TestUpstreamTraceStoredAndChainVerifies(t *testing.T) {
	c := storetest.NewTestClient(t)
	w := audit.EntWriter{C: c}
	ctx := audit.WithTrace(context.Background(), audit.NewTraceID())
	if err := w.Write(ctx, audit.Entry{Action: "plain"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(audit.WithUpstreamTrace(ctx, tpTrace), audit.Entry{Action: "corr"}); err != nil {
		t.Fatal(err)
	}
	r := c.AuditLog.Query().Where(auditlog.Action("corr")).OnlyX(ctx)
	if r.UpstreamTraceID != tpTrace || r.TraceID == tpTrace || r.TraceID != audit.TraceFrom(ctx) {
		t.Fatalf("row %+v", r)
	}
	if p := c.AuditLog.Query().Where(auditlog.Action("plain")).OnlyX(ctx); p.UpstreamTraceID != "" {
		t.Fatal("无上游时应为空")
	}
	if res, err := audit.Verify(ctx, c); err != nil || !res.OK || res.Checked != 2 {
		t.Fatalf("verify %+v %v", res, err)
	}
	// upstream_trace_id 纳入哈希：同一行换掉 upstream 后哈希必然不同
	if audit.HashForTest(r) == audit.HashForTest(withUpstream(r, "")) {
		t.Fatal("upstream_trace_id 应纳入哈希")
	}
}

func withUpstream(r *ent.AuditLog, v string) *ent.AuditLog {
	cp := *r
	cp.UpstreamTraceID = v
	return &cp
}
