// Package mcp 远程 MCP 客户端（FR-22）：Streamable HTTP / SSE，基于 mark3labs/mcp-go（MIT，D-24）。
//
// 所有出站都经统一出站客户端（FR-27）：调用方传入 egress.Client.HTTP()；本包在每个请求上补齐出站调用方
// （module=mcp，不声明 AuditedFailClosed，D-38 第 9 条），包括 mcp-go 内部用后台 context 发出的会话关闭请求。
// 会话按需建立：每次发现 / 调用都是 initialize → 操作 → 关闭，整体受超时约束。
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/SSStq1314/hengdesk/server/internal/egress"
)

// 传输方式。
const (
	TransportStreamableHTTP = "streamable_http"
	TransportSSE            = "sse"
)

// 风险等级（与 tools.risk_level 一致）。
const (
	RiskReadOnly = "read_only"
	RiskWrite    = "write"
	RiskHigh     = "high_risk"
)

// Server 一次会话需要的连接参数（鉴权头已解密）。
type Server struct {
	URL       string
	Transport string
	Headers   map[string]string
	Timeout   time.Duration
}

// Tool 发现到的工具。
type Tool struct {
	Name          string
	Title         string
	Description   string
	InputSchema   map[string]any
	Annotations   map[string]any
	SuggestedRisk string
	Hash          string
}

// Result 工具调用结果（文本化，交给模型）。
type Result struct {
	Text    string
	IsError bool
}

// Client 远程 MCP 客户端。
type Client struct {
	HTTP *http.Client // 必须是统一出站客户端 egress.Client.HTTP()
}

// callerRT 在请求上下文缺少出站调用方时补上（mcp-go 关闭会话时用后台 context），并强制关闭 AuditedFailClosed。
type callerRT struct {
	base   http.RoundTripper
	caller egress.Caller
}

func (t callerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	cl := egress.CallerFrom(r.Context())
	if cl.Module == "" {
		cl = t.caller
	}
	// 无论上游怎样标记，MCP 出站一律不声明 AuditedFailClosed（D-38 第 9 条）。
	cl.AuditedFailClosed = false
	return t.base.RoundTrip(r.WithContext(egress.WithCaller(r.Context(), cl)))
}

func (c *Client) httpClient(ctx context.Context) *http.Client {
	base := c.HTTP.Transport
	cl := egress.CallerFrom(ctx)
	if cl.Module == "" {
		cl.Module = "mcp"
	}
	cl.AuditedFailClosed = false // D-38 第 9 条：MCP 放行日志写失败一律拒绝
	hc := *c.HTTP
	hc.Transport = callerRT{base: base, caller: cl}
	return &hc
}

// session 建立会话并完成 initialize；调用方负责 Close。
func (c *Client) session(ctx context.Context, s Server) (*client.Client, error) {
	// 必须是统一出站客户端（egress.Client.HTTP()）；没有 Transport 就拒绝，绝不回落到默认传输。
	if c == nil || c.HTTP == nil || c.HTTP.Transport == nil {
		return nil, errors.New("mcp: 未配置出站客户端")
	}
	hc := c.httpClient(ctx)
	var (
		cli *client.Client
		err error
	)
	switch s.Transport {
	case TransportSSE:
		cli, err = client.NewSSEMCPClient(s.URL, transport.WithHTTPClient(hc), transport.WithHeaders(s.Headers))
	case TransportStreamableHTTP, "":
		cli, err = client.NewStreamableHttpClient(s.URL, transport.WithHTTPBasicClient(hc),
			transport.WithHTTPHeaders(s.Headers), transport.WithHTTPTimeout(timeoutOf(s)))
	default:
		return nil, fmt.Errorf("mcp: 不支持的传输方式 %q", s.Transport)
	}
	if err != nil {
		return nil, err
	}
	if err := cli.Start(ctx); err != nil {
		_ = cli.Close()
		return nil, err
	}
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "hengdesk", Version: "m1"}
	if _, err := cli.Initialize(ctx, req); err != nil {
		_ = cli.Close()
		return nil, err
	}
	return cli, nil
}

func timeoutOf(s Server) time.Duration {
	if s.Timeout <= 0 {
		return 30 * time.Second
	}
	return s.Timeout
}

// ListTools 发现工具（分页取全）。
func (c *Client) ListTools(ctx context.Context, s Server) ([]Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutOf(s))
	defer cancel()
	cli, err := c.session(ctx, s)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cli.Close() }()
	var out []Tool
	var cursor mcp.Cursor
	for page := 0; page < 50; page++ {
		req := mcp.ListToolsRequest{}
		req.Params.Cursor = cursor
		res, err := cli.ListTools(ctx, req)
		if err != nil {
			return nil, err
		}
		for _, t := range res.Tools {
			out = append(out, convert(t))
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CallTool 调用工具。工具自身报错（isError）作为结果返回，不当成传输错误。
func (c *Client) CallTool(ctx context.Context, s Server, name string, args map[string]any) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutOf(s))
	defer cancel()
	cli, err := c.session(ctx, s)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cli.Close() }()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := cli.CallTool(ctx, req)
	if err != nil {
		return nil, err
	}
	return &Result{Text: resultText(res), IsError: res.IsError}, nil
}

func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, ct := range res.Content {
		switch v := ct.(type) {
		case mcp.TextContent:
			parts = append(parts, v.Text)
		case *mcp.TextContent:
			parts = append(parts, v.Text)
		default:
			if b, err := json.Marshal(v); err == nil {
				parts = append(parts, string(b))
			}
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(b))
		}
	}
	return strings.Join(parts, "\n")
}

func convert(t mcp.Tool) Tool {
	out := Tool{Name: t.Name, Title: t.Title, Description: t.Description}
	if b, err := json.Marshal(t); err == nil {
		var m struct {
			InputSchema map[string]any `json:"inputSchema"`
			Annotations map[string]any `json:"annotations"`
		}
		if json.Unmarshal(b, &m) == nil {
			out.InputSchema, out.Annotations = m.InputSchema, m.Annotations
		}
	}
	if out.InputSchema == nil {
		out.InputSchema = map[string]any{"type": "object"}
	}
	out.SuggestedRisk = SuggestRisk(t.Annotations)
	out.Hash = Hash(out.Name, out.Description, out.InputSchema)
	return out
}

// SuggestRisk 按 MCP 注解给出建议风险等级（只是建议，管理员确认；审批只看平台策略，D-27）：
// readOnlyHint=true → 只读；明确 destructiveHint=false → 写入；其余（含未标注，MCP 规范默认视为破坏性）→ 高危。
func SuggestRisk(a mcp.ToolAnnotation) string {
	switch {
	case a.ReadOnlyHint != nil && *a.ReadOnlyHint:
		return RiskReadOnly
	case a.DestructiveHint != nil && !*a.DestructiveHint:
		return RiskWrite
	default:
		return RiskHigh
	}
}

// Hash 工具名称 + 描述 + 入参 schema 的规范化哈希（encoding/json 对 map 键排序，结果稳定）。
// 描述也纳入：描述会影响模型行为（工具投毒），变化同样需要复核。
func Hash(name, desc string, schema map[string]any) string {
	b, _ := json.Marshal(struct {
		N string         `json:"n"`
		D string         `json:"d"`
		S map[string]any `json:"s"`
	}{name, desc, schema})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
