// Package mockmcp 本地假 MCP server（演示剧本「工单系统」），仅供测试、CI 与离线演示使用。
//
// 提供 query_ticket（只读）与 create_ticket（写入）两个工具，统计每个工具实际收到的调用次数与参数，
// 用于验证「审批通过前服务端收到 0 次请求，通过后用原参数恰好执行 1 次」（AC-06）。
// 同时挂 Streamable HTTP（/mcp）与 SSE（/sse、/message）两种传输。
package mockmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Mock 假工单 MCP server。
type Mock struct {
	MCP   *server.MCPServer
	Token string // 非空时要求 Authorization: Bearer <Token>

	mu      sync.Mutex
	calls   map[string][]map[string]any
	nextID  atomic.Int64
	delay   atomic.Int64 // 每次调用前休眠的毫秒数（测试超时用）
	authErr atomic.Int64
}

// New 构造并注册默认工具。
func New(token string) *Mock {
	m := &Mock{Token: token, calls: map[string][]map[string]any{}}
	m.nextID.Store(1000)
	m.MCP = server.NewMCPServer("mock-ticket", "1.0.0", server.WithToolCapabilities(true))
	m.SetTools(false)
	return m
}

// SetTools 重置工具集；v2=true 时 create_ticket 多一个必填参数 priority（模拟 schema 变化），且下线 query_ticket。
func (m *Mock) SetTools(v2 bool) {
	create := []mcp.ToolOption{
		mcp.WithDescription("在工单系统中新建工单"),
		mcp.WithString("title", mcp.Required(), mcp.Description("工单标题")),
		mcp.WithString("content", mcp.Description("工单内容")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
	}
	if v2 {
		create = append(create, mcp.WithString("priority", mcp.Required(), mcp.Description("优先级")))
	}
	tools := []server.ServerTool{{Tool: mcp.NewTool("create_ticket", create...), Handler: m.createTicket}}
	if !v2 {
		tools = append(tools, server.ServerTool{Tool: mcp.NewTool("query_ticket",
			mcp.WithDescription("按工单号查询工单状态"),
			mcp.WithString("ticket_id", mcp.Required(), mcp.Description("工单号")),
			mcp.WithReadOnlyHintAnnotation(true),
		), Handler: m.queryTicket})
	}
	m.MCP.SetTools(tools...)
}

// SetDelay 每次工具调用前等待 d（模拟超时）。
func (m *Mock) SetDelay(d time.Duration) { m.delay.Store(int64(d / time.Millisecond)) }

func (m *Mock) record(name string, args map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls[name] = append(m.calls[name], args)
}

// Calls 返回某个工具收到的全部调用参数（副本）。
func (m *Mock) Calls(name string) []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]map[string]any(nil), m.calls[name]...)
}

// AuthFailures 鉴权失败的请求次数。
func (m *Mock) AuthFailures() int { return int(m.authErr.Load()) }

func (m *Mock) wait(ctx context.Context) {
	if d := m.delay.Load(); d > 0 {
		select {
		case <-time.After(time.Duration(d) * time.Millisecond):
		case <-ctx.Done():
		}
	}
}

func (m *Mock) queryTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	m.wait(ctx)
	args := req.GetArguments()
	m.record("query_ticket", args)
	id := req.GetString("ticket_id", "")
	if id == "" {
		return mcp.NewToolResultError("缺少 ticket_id"), nil
	}
	b, _ := json.Marshal(map[string]any{"ticket_id": id, "status": "处理中", "assignee": "IT 服务台"})
	return mcp.NewToolResultText(string(b)), nil
}

func (m *Mock) createTicket(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	m.wait(ctx)
	args := req.GetArguments()
	m.record("create_ticket", args)
	title := req.GetString("title", "")
	if title == "" {
		return mcp.NewToolResultError("缺少 title"), nil
	}
	id := fmt.Sprintf("T-%d", m.nextID.Add(1))
	b, _ := json.Marshal(map[string]any{"ticket_id": id, "title": title, "status": "已创建"})
	return mcp.NewToolResultText(string(b)), nil
}

// Handler Streamable HTTP 挂在 /mcp，SSE 挂在 /sse（消息端点 /message）。
func (m *Mock) Handler() http.Handler {
	mux := http.NewServeMux()
	st := server.NewStreamableHTTPServer(m.MCP, server.WithDisableLocalhostProtection(true))
	sse := server.NewSSEServer(m.MCP, server.WithUseFullURLForMessageEndpoint(false),
		server.WithSSEEndpoint("/sse"), server.WithMessageEndpoint("/message"))
	mux.Handle("/mcp", st)
	mux.Handle("/sse", sse.SSEHandler())
	mux.Handle("/message", sse.MessageHandler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Token != "" && r.URL.Path != "/healthz" && r.Header.Get("Authorization") != "Bearer "+m.Token &&
			!strings.HasPrefix(r.URL.Path, "/message") { // SSE 消息端点靠会话 ID 关联，鉴权在 /sse 建立时完成
			m.authErr.Add(1)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Server 测试用：启动 httptest server。
type Server struct {
	*Mock
	*httptest.Server
}

// Start 启动测试 server（调用方 Close）。
func Start(token string) *Server {
	m := New(token)
	return &Server{Mock: m, Server: httptest.NewServer(m.Handler())}
}
