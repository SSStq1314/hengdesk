package mcp_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/SSStq1314/hengdesk/server/internal/egress"
	"github.com/SSStq1314/hengdesk/server/internal/mcp"
	"github.com/SSStq1314/hengdesk/server/internal/mcp/mockmcp"
)

type sink struct {
	mu   sync.Mutex
	recs []egress.Record
	fail bool
}

func (s *sink) Start(_ context.Context, r egress.Record) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return 0, errors.New("log store down")
	}
	s.recs = append(s.recs, r)
	return len(s.recs), nil
}

func (s *sink) Write(_ context.Context, r egress.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r)
	return nil
}

func newClient(p egress.Policy) (*mcp.Client, *sink) {
	s := &sink{}
	return &mcp.Client{HTTP: (&egress.Client{Policy: p, Sink: s}).HTTP()}, s
}

func TestListAndCallBothTransports(t *testing.T) {
	srv := mockmcp.Start("tok-123")
	defer srv.Close()
	c, s := newClient(egress.MustStatic("cidr 127.0.0.0/8 *"))
	ctx := egress.WithCaller(context.Background(), egress.Caller{UserID: 5, Module: "mcp", AuditedFailClosed: true})
	for _, tc := range []struct{ tr, path string }{{mcp.TransportStreamableHTTP, "/mcp"}, {mcp.TransportSSE, "/sse"}} {
		sv := mcp.Server{URL: srv.URL + tc.path, Transport: tc.tr, Headers: map[string]string{"Authorization": "Bearer tok-123"}, Timeout: 5 * time.Second}
		tools, err := c.ListTools(ctx, sv)
		if err != nil {
			t.Fatalf("%s list: %v", tc.tr, err)
		}
		if len(tools) != 2 || tools[0].Name != "create_ticket" || tools[1].Name != "query_ticket" {
			t.Fatalf("%s tools %+v", tc.tr, tools)
		}
		if tools[0].SuggestedRisk != mcp.RiskWrite || tools[1].SuggestedRisk != mcp.RiskReadOnly || tools[0].Hash == "" {
			t.Fatalf("%s risk %+v", tc.tr, tools)
		}
		res, err := c.CallTool(ctx, sv, "query_ticket", map[string]any{"ticket_id": "T-1"})
		if err != nil || res.IsError || res.Text == "" {
			t.Fatalf("%s call %v %+v", tc.tr, err, res)
		}
		res, err = c.CallTool(ctx, sv, "create_ticket", map[string]any{})
		if err != nil || !res.IsError {
			t.Fatalf("%s tool error should be a result: %v %+v", tc.tr, err, res)
		}
	}
	if n := len(srv.Calls("query_ticket")); n != 2 {
		t.Fatalf("query calls %d", n)
	}
	// 出站记录全部标为 mcp 模块且强制不声明 AuditedFailClosed（包括关闭会话的请求）
	s.mu.Lock()
	recs := append([]egress.Record(nil), s.recs...)
	s.mu.Unlock()
	if len(recs) == 0 {
		t.Fatal("no egress records")
	}
	for _, r := range recs {
		if r.Caller.Module != "mcp" || r.Caller.AuditedFailClosed || r.Caller.UserID != 5 {
			t.Fatalf("caller %+v", r.Caller)
		}
	}
	// 错误 token：失败
	if _, err := c.ListTools(ctx, mcp.Server{URL: srv.URL + "/mcp"}); err == nil {
		t.Fatal("no auth should fail")
	}
}

func TestEgressDeniedAndLogFailClosed(t *testing.T) {
	srv := mockmcp.Start("")
	defer srv.Close()
	c, _ := newClient(egress.MustStatic("host api.example.com"))
	_, err := c.ListTools(context.Background(), mcp.Server{URL: srv.URL + "/mcp"})
	var de *egress.DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("want DeniedError, got %v", err)
	}
	// 放行日志写不进去：即使调用方声明了 AuditedFailClosed，MCP 也一律拒绝（D-38 第 9 条）
	c2, s := newClient(egress.MustStatic("cidr 127.0.0.0/8 *"))
	s.fail = true
	ctx := egress.WithCaller(context.Background(), egress.Caller{Module: "mcp", AuditedFailClosed: true})
	if _, err := c2.ListTools(ctx, mcp.Server{URL: srv.URL + "/mcp"}); err == nil {
		t.Fatal("log failure must deny")
	}
	if len(srv.Calls("query_ticket")) != 0 {
		t.Fatal("server reached")
	}
}

func TestTimeout(t *testing.T) {
	srv := mockmcp.Start("")
	defer srv.Close()
	srv.SetDelay(2 * time.Second)
	c, _ := newClient(egress.MustStatic("cidr 127.0.0.0/8 *"))
	start := time.Now()
	_, err := c.CallTool(context.Background(), mcp.Server{URL: srv.URL + "/mcp", Timeout: 300 * time.Millisecond}, "query_ticket", map[string]any{"ticket_id": "1"})
	if err == nil || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("timeout err=%v after %s", err, time.Since(start))
	}
}

func TestSuggestRiskAndHash(t *testing.T) {
	tr, fa := true, false
	if mcp.SuggestRisk(mcpgo.ToolAnnotation{}) != mcp.RiskHigh ||
		mcp.SuggestRisk(mcpgo.ToolAnnotation{ReadOnlyHint: &tr}) != mcp.RiskReadOnly ||
		mcp.SuggestRisk(mcpgo.ToolAnnotation{ReadOnlyHint: &fa, DestructiveHint: &fa}) != mcp.RiskWrite ||
		mcp.SuggestRisk(mcpgo.ToolAnnotation{DestructiveHint: &tr}) != mcp.RiskHigh {
		t.Fatal("suggest risk")
	}
	a := mcp.Hash("x", "d", map[string]any{"a": 1, "b": map[string]any{"c": 2}})
	b := mcp.Hash("x", "d", map[string]any{"b": map[string]any{"c": 2}, "a": 1})
	if a != b || a == mcp.Hash("x", "d2", map[string]any{"a": 1}) {
		t.Fatal("hash")
	}
}
