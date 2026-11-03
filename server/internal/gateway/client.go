package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/SSStq1314/hengdesk/server/internal/egress"
)

// Upstream 一次调用使用的上游配置（密钥已解密，只在内存中）。
type Upstream struct {
	Name    string
	BaseURL string
	APIKey  string
	Kind    string // openai_compat / anthropic
	Dialect string // generic / deepseek / dashscope / vllm
	Timeout time.Duration
}

// ModelConfig 一次调用使用的模型配置。
type ModelConfig struct {
	UpstreamModel    string
	Thinking         string // off / on / provider_default
	ToolChoicePolicy string // downgrade / reject
	MaxOutputTokens  int
}

// Client OpenAI 兼容适配器。
type Client struct {
	HTTP *http.Client
}

var reFuncName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// thinkingOn 该次调用上游是否处于思考模式（DeepSeek 不传 thinking 时默认开启）。
// 通义（dashscope）与 vLLM 的思考模型同样不支持强制工具调用，只在显式开启时按思考模式处理。
func thinkingOn(up Upstream, m ModelConfig) bool {
	switch up.Dialect {
	case "deepseek":
		return m.Thinking == "on" || m.Thinking == "provider_default"
	case "dashscope", "vllm":
		return m.Thinking == "on"
	}
	return false
}

// BuildBody 生成上游请求体，并处理 DeepSeek 方言：
//   - 显式发送 thinking 开关（provider_default 除外）；
//   - 思考模式下 tool_choice=required / 指定函数会被上游 400：按模型策略降级为 auto 或直接报错；
//   - generic 方言去掉历史消息里的 reasoning_content，deepseek 方言原样回传；
//   - 流式一律带 stream_options.include_usage。
func BuildBody(req *ChatRequest, up Upstream, m ModelConfig) (map[string]any, []string, *Error) {
	if gerr := validateRequest(req); gerr != nil {
		return nil, nil, gerr
	}
	var warnings []string
	body := map[string]any{"model": m.UpstreamModel}
	msgs := make([]Message, len(req.Messages))
	for i, mm := range req.Messages {
		mm.ReasoningSignature = ""
		if up.Dialect != "deepseek" {
			mm.ReasoningContent = nil
		}
		msgs[i] = mm
	}
	body["messages"] = msgs
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
		tc := req.ToolChoice
		var s string
		forced := json.Unmarshal(tc, &s) != nil || s == "required"
		if forced && thinkingOn(up, m) {
			if m.ToolChoicePolicy == "reject" {
				return nil, nil, newErr(http.StatusBadRequest, CodeToolChoice,
					"模型处于思考模式，不支持 tool_choice=required 或指定函数；请改用 auto，或使用关闭思考模式的模型")
			}
			tc = json.RawMessage(`"auto"`)
			warnings = append(warnings, "tool_choice_downgraded: 思考模式不支持 required / 指定函数，已降级为 auto")
		}
		body["tool_choice"] = tc
	}
	if m.Thinking == "on" || m.Thinking == "off" {
		on := m.Thinking == "on"
		switch up.Dialect {
		case "deepseek":
			if on {
				body["thinking"] = map[string]string{"type": "enabled"}
			} else {
				body["thinking"] = map[string]string{"type": "disabled"}
			}
		case "dashscope":
			body["enable_thinking"] = on
		case "vllm":
			body["chat_template_kwargs"] = map[string]bool{"enable_thinking": on}
		}
	}
	if req.Stream {
		body["stream"] = true
		body["stream_options"] = map[string]bool{"include_usage": true}
	}
	if req.MaxTokens > 0 {
		if m.MaxOutputTokens > 0 && req.MaxTokens > m.MaxOutputTokens {
			return nil, nil, newErr(http.StatusBadRequest, CodeBadRequest, "max_tokens 超过该模型上限 %d", m.MaxOutputTokens)
		}
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		body["stop"] = req.Stop
	}
	return body, warnings, nil
}

// validateRequest 与上游无关的请求校验（OpenAI 兼容与 Anthropic 共用）。
func validateRequest(req *ChatRequest) *Error {
	if len(req.Messages) == 0 {
		return newErr(http.StatusBadRequest, CodeBadRequest, "messages 不能为空")
	}
	for _, t := range req.Tools {
		if t.Type != "function" {
			return newErr(http.StatusBadRequest, CodeBadRequest, "只支持 type=function 的工具")
		}
		var f struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(t.Function, &f) != nil || !reFuncName.MatchString(f.Name) {
			return newErr(http.StatusBadRequest, CodeInvalidFunctionName,
				"工具函数名 %q 不合法：只能包含字母、数字、下划线、连字符，长度 1~64", f.Name)
		}
	}
	return nil
}

// do 发送上游请求。HTTP 必须是出站控制客户端（egress.Client.HTTP()），不回退到 http.DefaultClient。
func (c *Client) do(ctx context.Context, r *http.Request, key string) (*http.Response, *Error) {
	if c.HTTP == nil {
		return nil, newErr(http.StatusInternalServerError, CodeUpstreamUnavailable, "出站客户端未配置")
	}
	resp, err := c.HTTP.Do(r)
	if err != nil {
		var de *egress.DeniedError
		if errors.As(err, &de) {
			return nil, newErr(http.StatusForbidden, CodeEgressDenied, "%s", de.Message())
		}
		if ctx.Err() != nil || isTimeout(err) {
			return nil, newErr(http.StatusGatewayTimeout, CodeUpstreamTimeout, "上游响应超时")
		}
		e := newErr(http.StatusBadGateway, CodeUpstreamUnavailable, "无法连接上游：%s", Redact(err.Error(), key))
		e.Retryable = true
		return nil, e
	}
	return resp, nil
}

func (c *Client) post(ctx context.Context, up Upstream, body map[string]any) (*http.Response, *Error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, newErr(http.StatusBadRequest, CodeBadRequest, "请求无法序列化")
	}
	url := strings.TrimRight(up.BaseURL, "/") + "/chat/completions"
	if up.Kind == KindAnthropic {
		url = anthropicURL(up, "/messages")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, newErr(http.StatusBadGateway, CodeUpstreamUnavailable, "上游地址无效")
	}
	r.Header.Set("Content-Type", "application/json")
	if up.Kind == KindAnthropic {
		anthropicHeaders(r, up)
	} else if up.APIKey != "" {
		r.Header.Set("Authorization", "Bearer "+up.APIKey)
	}
	resp, gerr := c.do(ctx, r, up.APIKey)
	if gerr != nil {
		return nil, gerr
	}
	if resp.StatusCode/100 != 2 {
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, upstreamError(resp.StatusCode, raw, up.APIKey)
	}
	return resp, nil
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &t) && t.Timeout())
}

func timeoutOf(up Upstream) time.Duration {
	if up.Timeout > 0 {
		return up.Timeout
	}
	return 120 * time.Second
}

// Chat 非流式调用。
func (c *Client) Chat(ctx context.Context, req *ChatRequest, up Upstream, m ModelConfig) (*ChatResponse, *Error) {
	if up.Kind == KindAnthropic {
		return c.anthropicChat(ctx, req, up, m)
	}
	r := *req
	r.Stream = false
	body, warns, gerr := BuildBody(&r, up, m)
	if gerr != nil {
		return nil, gerr
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutOf(up))
	defer cancel()
	resp, gerr := c.post(ctx, up, body)
	if gerr != nil {
		return nil, gerr
	}
	defer func() { _ = resp.Body.Close() }()
	var w wireResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&w); err != nil {
		if ctx.Err() != nil {
			return nil, newErr(http.StatusGatewayTimeout, CodeUpstreamTimeout, "上游响应超时")
		}
		return nil, newErr(http.StatusBadGateway, CodeUpstreamProtocol, "上游返回的不是合法 JSON")
	}
	if len(w.Choices) == 0 || w.Choices[0].Message == nil {
		return nil, newErr(http.StatusBadGateway, CodeUpstreamProtocol, "上游响应缺少 choices")
	}
	ch := w.Choices[0]
	out := &ChatResponse{ID: w.ID, Model: w.Model, Warnings: warns, Message: fromWire(ch.Message)}
	if ch.FinishReason != nil {
		out.FinishReason = *ch.FinishReason
	}
	if w.Usage != nil {
		out.Usage = w.Usage.toUsage()
	} else {
		out.Usage = Estimate(req.Messages, out.Message)
	}
	return out, nil
}

func fromWire(wm *wireMessage) Message {
	m := Message{Role: "assistant", ReasoningContent: wm.reasoning(), ToolCalls: wm.ToolCalls}
	if wm.Content != nil {
		m.Content, _ = json.Marshal(*wm.Content)
	} else {
		m.Content = json.RawMessage("null")
	}
	for i := range m.ToolCalls {
		m.ToolCalls[i].Index = nil
	}
	return m
}

// Stream 流式调用：每个增量回调 onDelta，结束时返回聚合结果（含 usage）。
// usage 取任意 chunk 上最后一个非空值（DeepSeek 放在带 finish_reason 的最后一个 chunk 上，
// Ollama / vLLM 放在单独的 choices:[] chunk 上）；全程没有则估算并标记 Estimated。
// tool_calls 按 index 拼接 arguments，兼容整段到达和逐 token 增量。
func (c *Client) Stream(ctx context.Context, req *ChatRequest, up Upstream, m ModelConfig, onDelta func(Delta) error) (*ChatResponse, *Error) {
	if up.Kind == KindAnthropic {
		return c.anthropicStream(ctx, req, up, m, onDelta)
	}
	r := *req
	r.Stream = true
	body, warns, gerr := BuildBody(&r, up, m)
	if gerr != nil {
		return nil, gerr
	}
	// 首包与空闲超时：任意两次读到数据之间超过 timeout 即取消。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timedOut atomic.Bool
	idle := timeoutOf(up)
	timer := time.AfterFunc(idle, func() { timedOut.Store(true); cancel() })
	defer timer.Stop()

	resp, gerr := c.post(ctx, up, body)
	if gerr != nil {
		return nil, gerr
	}
	defer func() { _ = resp.Body.Close() }()

	out := &ChatResponse{Warnings: warns}
	var content, reasoning strings.Builder
	sawReasoning := false
	calls := map[int]*ToolCall{}
	var usage *wireUsage
	// fail 流已开始后出错：附上已产生的用量（上游给过 usage 就用上游的，否则按已收到的内容估算并标记 Estimated），
	// 供计量记录中断前消耗的 token。
	fail := func(e *Error) (*ChatResponse, *Error) {
		var u Usage
		if usage != nil {
			u = usage.toUsage()
		} else {
			part := Message{Role: "assistant"}
			part.Content, _ = json.Marshal(content.String())
			if reasoning.Len() > 0 {
				r := reasoning.String()
				part.ReasoningContent = &r
			}
			u = Estimate(req.Messages, part)
		}
		e.Usage = &u
		return nil, e
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	done := false
	for sc.Scan() {
		timer.Reset(idle)
		line := strings.TrimRight(sc.Text(), "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			break
		}
		var w wireResponse
		if err := json.Unmarshal([]byte(data), &w); err != nil {
			// 有些上游在流里发 {"error":...}
			return fail(newErr(http.StatusBadGateway, CodeUpstreamProtocol, "上游流式数据无法解析"))
		}
		if w.ID != "" {
			out.ID = w.ID
		}
		if w.Model != "" {
			out.Model = w.Model
		}
		if w.Usage != nil {
			usage = w.Usage
		}
		for _, ch := range w.Choices {
			if ch.Index != 0 {
				continue
			}
			d := Delta{}
			if ch.Delta != nil {
				if ch.Delta.Content != nil {
					d.Content = *ch.Delta.Content
					content.WriteString(d.Content)
				}
				if rc := ch.Delta.reasoning(); rc != nil {
					sawReasoning = true
					d.ReasoningContent = *rc
					reasoning.WriteString(d.ReasoningContent)
				}
				for _, tc := range ch.Delta.ToolCalls {
					idx := 0
					if tc.Index != nil {
						idx = *tc.Index
					}
					agg := calls[idx]
					if agg == nil {
						agg = &ToolCall{Type: "function"}
						calls[idx] = agg
					}
					if tc.ID != "" {
						agg.ID = tc.ID
					}
					if tc.Type != "" {
						agg.Type = tc.Type
					}
					if tc.Function.Name != "" {
						agg.Function.Name = tc.Function.Name
					}
					agg.Function.Arguments += tc.Function.Arguments
					i := idx
					tc.Index = &i
					d.ToolCalls = append(d.ToolCalls, tc)
				}
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				d.FinishReason = *ch.FinishReason
				out.FinishReason = d.FinishReason
			}
			if d.Content != "" || d.ReasoningContent != "" || len(d.ToolCalls) > 0 || d.FinishReason != "" {
				if err := onDelta(d); err != nil {
					return fail(newErr(499, "client_closed", "调用方已断开"))
				}
			}
		}
	}
	if err := sc.Err(); err != nil || !done {
		if timedOut.Load() {
			return fail(newErr(http.StatusGatewayTimeout, CodeUpstreamTimeout, "上游流式响应超时（%s 内没有收到数据）", idle))
		}
		if ctx.Err() != nil && !timedOut.Load() {
			return fail(newErr(499, "client_closed", "调用方已断开"))
		}
		if err != nil {
			return fail(newErr(http.StatusBadGateway, CodeUpstreamUnavailable, "上游流中断"))
		}
		if out.FinishReason == "" {
			return fail(newErr(http.StatusBadGateway, CodeUpstreamProtocol, "上游流未正常结束"))
		}
	}
	msg := Message{Role: "assistant"}
	if content.Len() > 0 || len(calls) == 0 {
		msg.Content, _ = json.Marshal(content.String())
	} else {
		msg.Content = json.RawMessage("null")
	}
	if sawReasoning {
		s := reasoning.String()
		msg.ReasoningContent = &s
	}
	idxs := make([]int, 0, len(calls))
	for i := range calls {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		msg.ToolCalls = append(msg.ToolCalls, *calls[i])
	}
	for _, tc := range msg.ToolCalls {
		if tc.Function.Arguments != "" && !json.Valid([]byte(tc.Function.Arguments)) {
			out.Warnings = append(out.Warnings, "tool_arguments_invalid_json: "+tc.Function.Name)
		}
	}
	out.Message = msg
	if usage != nil {
		out.Usage = usage.toUsage()
	} else {
		out.Usage = Estimate(req.Messages, msg)
	}
	return out, nil
}

// Estimate 上游未返回 usage 时的粗略估算（PRD：标记为估算值）：ASCII 约 4 字符 1 token，其他字符约 1 字 1 token。
// 后续 PR 换成按模型的 tokenizer。
func Estimate(prompt []Message, completion Message) Usage {
	count := func(s string) int {
		ascii, other := 0, 0
		for _, r := range s {
			if r < utf8.RuneSelf {
				ascii++
			} else {
				other++
			}
		}
		return ascii/4 + other + 1
	}
	text := func(m Message) string {
		var s strings.Builder
		s.Write(m.Content)
		for _, tc := range m.ToolCalls {
			s.WriteString(tc.Function.Name + tc.Function.Arguments)
		}
		if m.ReasoningContent != nil {
			s.WriteString(*m.ReasoningContent)
		}
		return s.String()
	}
	u := Usage{Estimated: true}
	for _, m := range prompt {
		u.PromptTokens += count(text(m)) + 3
	}
	u.CompletionTokens = count(text(completion))
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
	return u
}

// ListModels 调用上游 GET {base}/models，用于管理员「测试连接」：验证地址可达、Key 有效，返回上游可用模型 ID。
func (c *Client) ListModels(ctx context.Context, up Upstream) ([]string, *Error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutOf(up))
	defer cancel()
	url := strings.TrimRight(up.BaseURL, "/") + "/models"
	if up.Kind == KindAnthropic {
		url = anthropicURL(up, "/models")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, newErr(http.StatusBadGateway, CodeUpstreamUnavailable, "上游地址无效")
	}
	if up.Kind == KindAnthropic {
		anthropicHeaders(r, up)
	} else if up.APIKey != "" {
		r.Header.Set("Authorization", "Bearer "+up.APIKey)
	}
	resp, gerr := c.do(ctx, r, up.APIKey)
	if gerr != nil {
		return nil, gerr
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, upstreamError(resp.StatusCode, raw, up.APIKey)
	}
	var w struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, newErr(http.StatusBadGateway, CodeUpstreamProtocol, "上游 /models 返回无法解析")
	}
	ids := make([]string, 0, len(w.Data))
	for _, d := range w.Data {
		ids = append(ids, d.ID)
	}
	return ids, nil
}
