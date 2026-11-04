// Package mockupstream 测试用的 OpenAI 兼容上游，复现 DeepSeek 实测行为（docs/spikes/model-compat.md）：
//   - 模型名只认 deepseek-flash / deepseek-v4-pro；
//   - 不传 thinking 时默认开启思考；思考模式下 tool_choice=required / 指定函数返回 400；
//   - 流式 usage 放在最后一个带 finish_reason 的 chunk 上（不论是否 include_usage），之后 [DONE]；
//   - 思考模式先出 reasoning_content，再出 content / tool_calls；工具参数逐 token 增量；
//   - 带工具的多轮对话里，历史 assistant 工具消息缺少 reasoning_content 时返回 400（按官方文档的严格行为）；
//   - 无效 key 返回 401，message 回显 key 末 4 位。
//
// 只供测试与本地演示，不要用于生产。
package mockupstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Server 模拟上游。
type Server struct {
	*httptest.Server
	Key string

	mu sync.Mutex
	// Requests 收到的请求体（按顺序）。
	Requests []map[string]any
	// FailStatus 非 0 时所有 chat 请求直接返回该状态码。
	FailStatus int
	// FailTimes 非 0 时只有前 FailTimes 次 chat 请求返回 FailStatus，之后正常（用于重试测试）。
	FailTimes int
	// StallAfter 流式输出 N 个内容字符后挂起，直到客户端断开（用于停止生成测试）。
	StallAfter int
	// Delay 返回前等待（用于超时测试）。
	Delay time.Duration
	// TokenDelay 流式输出时每个内容片段之间等待（压测模拟出字速度）。
	TokenDelay time.Duration
	// OmitUsage 不返回 usage（用于估算测试）。
	OmitUsage bool
	// EmbedCalls 收到的 /embeddings 请求数；EmbedFail 非 0 时 /embeddings 直接返回该状态码。
	EmbedCalls int
	EmbedFail  int
	// CutAfter 非 0 时流式回答输出 CutAfter 个字符后直接断开连接（用于首 token 之后失败的测试）。
	CutAfter int
}

// New 启动模拟上游；key 为期望的 API Key。
func New(key string) *Server {
	s := &Server{Key: key}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// NewHandler 只构造处理函数（以真实端口运行本地演示时使用）。
func NewHandler(key string) (*Server, http.Handler) {
	s := &Server{Key: key}
	return s, http.HandlerFunc(s.handle)
}

// LastRequest 最近一次请求体。
func (s *Server) LastRequest() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Requests) == 0 {
		return nil
	}
	return s.Requests[len(s.Requests)-1]
}

func writeErr(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"message": msg + " (request_id: mock-req-1)", "type": typ, "param": nil, "code": "invalid_request_error"}})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/models" || r.URL.Path == "/v1/models" {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{
			{"id": "deepseek-flash", "object": "model"}, {"id": "deepseek-v4-pro", "object": "model"}}})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/embeddings") {
		s.embeddings(w, r)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got != s.Key {
		tail := got
		if len(tail) > 4 {
			tail = tail[len(tail)-4:]
		}
		writeErr(w, 401, "authentication_error", "Authentication Fails, Your api key: ****"+tail+" is invalid")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid_request_error", "invalid json")
		return
	}
	s.mu.Lock()
	s.Requests = append(s.Requests, body)
	fail, delay, omit := s.FailStatus, s.Delay, s.OmitUsage
	if s.FailTimes > 0 {
		s.FailTimes--
		if s.FailTimes == 0 {
			s.FailStatus = 0
		}
	}
	s.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if fail != 0 {
		writeErr(w, fail, "server_error", "mock failure")
		return
	}
	model, _ := body["model"].(string)
	if model != "deepseek-flash" && model != "deepseek-v4-pro" {
		writeErr(w, 400, "invalid_request_error", "Model Not Exist. Supported: deepseek-flash, deepseek-v4-pro")
		return
	}
	thinking := true
	if t, ok := body["thinking"].(map[string]any); ok && t["type"] == "disabled" {
		thinking = false
	}
	tc := body["tool_choice"]
	forced := false
	if tc != nil {
		if str, ok := tc.(string); !ok || str == "required" {
			forced = true
		}
	}
	if thinking && forced {
		writeErr(w, 400, "invalid_request_error", "Thinking mode does not support this tool_choice")
		return
	}
	tools, _ := body["tools"].([]any)
	msgs, _ := body["messages"].([]any)
	if thinking && len(tools) > 0 {
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			if mm["role"] == "assistant" && mm["tool_calls"] != nil && mm["reasoning_content"] == nil {
				writeErr(w, 400, "invalid_request_error", "Missing `reasoning_content` field in the assistant message")
				return
			}
		}
	}
	lastRole := ""
	if len(msgs) > 0 {
		mm, _ := msgs[len(msgs)-1].(map[string]any)
		lastRole, _ = mm["role"].(string)
	}
	callTool := len(tools) > 0 && lastRole != "tool" && tc != "none"
	reasoning := ""
	if thinking {
		reasoning = "用户在问工单，先想一想。"
	}
	content := "你好，我是模拟模型。"
	if lastRole == "tool" {
		content = "工单 T-1 状态：处理中。"
	}
	usage := map[string]any{"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19,
		"prompt_cache_hit_tokens": 0, "prompt_cache_miss_tokens": 12,
		"prompt_tokens_details": map[string]any{"cached_tokens": 0}}
	if thinking {
		usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": 5}
	}
	id := "mock-cmpl-1"
	if stream, _ := body["stream"].(bool); stream {
		s.stream(w, r, id, model, reasoning, content, callTool, usage, omit)
		return
	}
	msg := map[string]any{"role": "assistant", "content": content}
	finish := "stop"
	if callTool {
		msg["content"] = ""
		msg["tool_calls"] = []any{map[string]any{"id": "call_00_mock", "type": "function",
			"function": map[string]any{"name": "get_ticket", "arguments": `{"id":"T-1"}`}}}
		finish = "tool_calls"
	}
	if thinking {
		msg["reasoning_content"] = reasoning
	}
	resp := map[string]any{"id": id, "object": "chat.completion", "model": model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}}}
	if !omit {
		resp["usage"] = usage
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, id, model, reasoning, content string, callTool bool, usage map[string]any, omit bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	send := func(delta map[string]any, finish any, u any) {
		chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": u}
		b, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	if reasoning != "" {
		send(map[string]any{"role": "assistant", "content": nil, "reasoning_content": ""}, nil, nil)
		for _, r := range reasoning {
			send(map[string]any{"content": nil, "reasoning_content": string(r)}, nil, nil)
		}
	} else {
		send(map[string]any{"role": "assistant", "content": ""}, nil, nil)
	}
	finish := "stop"
	if callTool {
		send(map[string]any{"content": nil, "tool_calls": []any{map[string]any{"index": 0, "id": "call_00_mock", "type": "function",
			"function": map[string]any{"name": "get_ticket", "arguments": ""}}}}, nil, nil)
		for _, p := range []string{`{"`, `id`, `":`, ` "`, `T`, `-`, `1`, `"}`} {
			send(map[string]any{"content": nil, "tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": p}}}}, nil, nil)
		}
		finish = "tool_calls"
	} else {
		s.mu.Lock()
		cut, stall, td := s.CutAfter, s.StallAfter, s.TokenDelay
		s.mu.Unlock()
		n := 0
		for _, ch := range content {
			if cut > 0 && n == cut {
				panic(http.ErrAbortHandler) // 模拟上游中途断开
			}
			if stall > 0 && n == stall {
				<-r.Context().Done()
				return
			}
			if td > 0 && n > 0 {
				time.Sleep(td)
			}
			send(map[string]any{"content": string(ch), "reasoning_content": nil}, nil, nil)
			n++
		}
	}
	var u any
	if !omit {
		u = usage
	}
	// DeepSeek：usage 与 finish_reason 在同一个最后 chunk 上，delta.content 为空串。
	send(map[string]any{"content": ""}, finish, u)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

// Count 已收到的 chat 请求数。
func (s *Server) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Requests)
}
