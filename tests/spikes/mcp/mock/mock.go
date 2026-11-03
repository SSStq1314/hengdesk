// Package mock 实现一个 mock 工单 MCP server（query_ticket / create_ticket / slow_op）。
package mock

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Server struct {
	MCP          *server.MCPServer
	CreateCalls  atomic.Int64 // create_ticket 在服务端真正执行的次数（用于验证 AC-06）
	mu           sync.Mutex
	tickets      map[string]string
	next         int
}

func New() *Server {
	s := &Server{tickets: map[string]string{"T-1": "打印机故障"}, next: 2}
	m := server.NewMCPServer("mock-ticket", "0.1.0", server.WithToolCapabilities(true))
	m.AddTool(mcp.NewTool("query_ticket",
		mcp.WithDescription("查询工单"),
		mcp.WithString("id", mcp.Required(), mcp.Description("工单号")),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := r.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		t, ok := s.tickets[id]
		if !ok {
			return mcp.NewToolResultError("工单不存在: " + id), nil // 业务错误：isError=true
		}
		return mcp.NewToolResultText(fmt.Sprintf("%s: %s", id, t)), nil
	})
	m.AddTool(mcp.NewTool("create_ticket",
		mcp.WithDescription("创建报销工单（有副作用，需审批）"),
		mcp.WithString("title", mcp.Required()),
		mcp.WithNumber("amount", mcp.Required()),
		mcp.WithDestructiveHintAnnotation(true),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		title, err := r.RequireString("title")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		s.CreateCalls.Add(1)
		s.mu.Lock()
		id := fmt.Sprintf("T-%d", s.next)
		s.next++
		s.tickets[id] = title
		s.mu.Unlock()
		return mcp.NewToolResultText("created " + id), nil
	})
	m.AddTool(mcp.NewTool("slow_op",
		mcp.WithDescription("睡眠 ms 毫秒，用于超时测试"),
		mcp.WithNumber("ms", mcp.Required()),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		select {
		case <-time.After(time.Duration(r.GetFloat("ms", 0)) * time.Millisecond):
			return mcp.NewToolResultText("done"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	s.MCP = m
	return s
}
