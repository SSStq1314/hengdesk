package spike

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"hengdesk-spikes/mcp/gateway"
	"hengdesk-spikes/mcp/mock"
)

type pol struct{ err error }

func (p pol) Decide(_ context.Context, _, tool string, _ map[string]any) (gateway.Decision, error) {
	if p.err != nil {
		return gateway.Deny, p.err
	}
	switch tool {
	case "query_ticket", "slow_op":
		return gateway.Allow, nil
	case "create_ticket":
		return gateway.NeedApproval, nil
	}
	return gateway.Deny, nil
}

type appr struct{ ans chan bool }

func (a appr) Await(ctx context.Context, _, _ string, _ map[string]any) (bool, error) {
	select {
	case v := <-a.ans:
		return v, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

type mem struct {
	mu   sync.Mutex
	ev   []gateway.AuditEvent
	fail bool
}

func (m *mem) Write(_ context.Context, e gateway.AuditEvent) error {
	if m.fail {
		return errors.New("db down")
	}
	m.mu.Lock()
	m.ev = append(m.ev, e)
	m.mu.Unlock()
	return nil
}

func run(t *testing.T, transport string) {
	ms := mock.New()
	var c *client.Client
	var err error
	switch transport {
	case "streamable":
		ts := httptest.NewServer(server.NewStreamableHTTPServer(ms.MCP))
		defer ts.Close()
		c, err = client.NewStreamableHttpClient(ts.URL)
	case "sse":
		ts := httptest.NewServer(nil)
		ts.Config.Handler = server.NewSSEServer(ms.MCP, server.WithBaseURL(ts.URL))
		defer ts.Close()
		c, err = client.NewSSEMCPClient(ts.URL + "/sse")
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ir := mcp.InitializeRequest{}
	ir.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	ir.Params.ClientInfo = mcp.Implementation{Name: "hengdesk-spike", Version: "0"}
	t0 := time.Now()
	init, err := c.Initialize(ctx, ir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("[%s] initialize ok: server=%s proto=%s (%v)", transport, init.ServerInfo.Name, init.ProtocolVersion, time.Since(t0))
	lt, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil || len(lt.Tools) != 3 {
		t.Fatalf("list tools: %v %d", err, len(lt.Tools))
	}
	for _, tl := range lt.Tools {
		t.Logf("  tool %s destructive=%v", tl.Name, tl.Annotations.DestructiveHint != nil && *tl.Annotations.DestructiveHint)
	}
	au := &mem{}
	ap := appr{ans: make(chan bool, 1)}
	g := &gateway.Gateway{C: c, Policy: pol{}, Approver: ap, Audit: au, CallTimeout: 300 * time.Millisecond, ApprovalTimeout: 200 * time.Millisecond}

	// 1 自动执行
	r, err := g.CallTool(ctx, "tr1", "A", "query_ticket", map[string]any{"id": "T-1"})
	if err != nil || r.IsError {
		t.Fatalf("query: %v", err)
	}
	t.Logf("  query_ticket -> %s", r.Content[0].(mcp.TextContent).Text)
	// 2 业务错误 isError
	r, err = g.CallTool(ctx, "tr1", "A", "query_ticket", map[string]any{"id": "nope"})
	if err != nil || !r.IsError {
		t.Fatalf("expected isError, got %v %+v", err, r)
	}
	// 3 未知工具 -> 协议错误（策略已拒绝；绕开网关直接调用验证协议层错误）
	req := mcp.CallToolRequest{}
	req.Params.Name = "no_such_tool"
	if _, err := c.CallTool(ctx, req); err == nil {
		t.Fatal("expected protocol error for unknown tool")
	} else {
		t.Logf("  unknown tool (raw client) -> err: %v", err)
	}
	if _, err := g.CallTool(ctx, "tr1", "A", "no_such_tool", nil); !errors.Is(err, gateway.ErrDenied) {
		t.Fatalf("gateway should deny unknown tool, got %v", err)
	}
	// 4 审批超时 -> 未执行
	if _, err := g.CallTool(ctx, "tr2", "A", "create_ticket", map[string]any{"title": "差旅", "amount": 100}); !errors.Is(err, gateway.ErrApprovalTO) {
		t.Fatalf("want timeout got %v", err)
	}
	// 5 拒绝 -> 未执行
	ap.ans <- false
	if _, err := g.CallTool(ctx, "tr2", "A", "create_ticket", map[string]any{"title": "差旅", "amount": 100}); !errors.Is(err, gateway.ErrRejected) {
		t.Fatalf("want rejected got %v", err)
	}
	if n := ms.CreateCalls.Load(); n != 0 {
		t.Fatalf("server executed %d before approval", n)
	}
	// 6 通过 -> 恰好执行一次
	ap.ans <- true
	r, err = g.CallTool(ctx, "tr2", "A", "create_ticket", map[string]any{"title": "差旅", "amount": 100})
	if err != nil || r.IsError || ms.CreateCalls.Load() != 1 {
		t.Fatalf("approved create failed: %v calls=%d", err, ms.CreateCalls.Load())
	}
	// 7 调用超时
	t0 = time.Now()
	_, err = g.CallTool(ctx, "tr3", "A", "slow_op", map[string]any{"ms": 2000})
	if err == nil {
		t.Fatal("expected timeout")
	}
	t.Logf("  slow_op timeout after %v: %v", time.Since(t0).Round(time.Millisecond), err)
	// 超时后连接仍可用
	if _, err := g.CallTool(ctx, "tr3", "A", "query_ticket", map[string]any{"id": "T-1"}); err != nil {
		t.Fatalf("client unusable after timeout: %v", err)
	}
	// 8 fail-closed：策略引擎出错 / 审计不可写
	g2 := *g
	g2.Policy = pol{err: errors.New("opa down")}
	if _, err := g2.CallTool(ctx, "tr4", "A", "query_ticket", map[string]any{"id": "T-1"}); !errors.Is(err, gateway.ErrPolicyFailed) {
		t.Fatalf("policy fail-closed: %v", err)
	}
	g3 := *g
	g3.Audit = &mem{fail: true}
	ap.ans <- true
	if _, err := g3.CallTool(ctx, "tr4", "A", "create_ticket", map[string]any{"title": "x", "amount": 1}); !errors.Is(err, gateway.ErrAuditFailed) {
		t.Fatalf("audit fail-closed: %v", err)
	}
	if ms.CreateCalls.Load() != 1 {
		t.Fatal("executed despite audit failure")
	}
	t.Logf("  audit events=%d, create_ticket server-side executions=%d", len(au.ev), ms.CreateCalls.Load())
	for _, e := range au.ev {
		t.Logf("    %s %s %-7s %s", e.TraceID, e.Tool, e.Stage, e.Result)
	}
}

func TestStreamableHTTP(t *testing.T) { run(t, "streamable") }
func TestSSE(t *testing.T)            { run(t, "sse") }
