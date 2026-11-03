package gateway

// Anthropic Messages 协议适配器（PR5）：把网关统一的 OpenAI 风格请求转换为 POST {base}/v1/messages，
// 再把响应（含 thinking、tool_use、usage）转换回统一结构。与 OpenAI 兼容适配器共用出站客户端、超时与脱敏。
// 依据 Anthropic 公开文档（Messages API / Streaming Messages / Extended thinking / Tool use），只用标准库。

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// KindAnthropic 供应商 kind。
const KindAnthropic = "anthropic"

// AnthropicVersion 请求头 anthropic-version。
const AnthropicVersion = "2023-06-01"

const (
	anthropicDefaultMaxTokens = 4096
	anthropicThinkingMin      = 1024 // 文档规定 budget_tokens 至少 1024
)

// anthropicURL base_url 可以写 https://api.anthropic.com 或 https://api.anthropic.com/v1。
func anthropicURL(up Upstream, path string) string {
	b := strings.TrimRight(up.BaseURL, "/")
	if !strings.HasSuffix(b, "/v1") {
		b += "/v1"
	}
	return b + path
}

func anthropicHeaders(r *http.Request, up Upstream) {
	r.Header.Set("anthropic-version", AnthropicVersion)
	if up.APIKey != "" {
		r.Header.Set("x-api-key", up.APIKey)
	}
}

type aBlock = map[string]any

// contentBlocks 把 OpenAI 风格 content（字符串 / null / 多模态数组）转换为 Anthropic 内容块。
func contentBlocks(raw json.RawMessage) ([]aBlock, *Error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return []aBlock{{"type": "text", "text": s}}, nil
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return nil, newErr(http.StatusBadRequest, CodeBadRequest, "content 只能是字符串或内容数组")
	}
	var out []aBlock
	for _, p := range parts {
		switch p.Type {
		case "text":
			out = append(out, aBlock{"type": "text", "text": p.Text})
		case "image_url":
			u := p.ImageURL.URL
			if strings.HasPrefix(u, "data:") {
				// data:image/png;base64,xxxx
				meta, data, ok := strings.Cut(strings.TrimPrefix(u, "data:"), ",")
				media, _, _ := strings.Cut(meta, ";")
				if !ok || !strings.HasSuffix(meta, ";base64") {
					return nil, newErr(http.StatusBadRequest, CodeBadRequest, "image_url 只支持 base64 data URL 或 http(s) URL")
				}
				out = append(out, aBlock{"type": "image", "source": map[string]string{"type": "base64", "media_type": media, "data": data}})
			} else {
				out = append(out, aBlock{"type": "image", "source": map[string]string{"type": "url", "url": u}})
			}
		default:
			return nil, newErr(http.StatusBadRequest, CodeBadRequest, "不支持的内容类型 %q", p.Type)
		}
	}
	return out, nil
}

func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	bs, _ := contentBlocks(raw)
	var b strings.Builder
	for _, x := range bs {
		if t, ok := x["text"].(string); ok {
			b.WriteString(t)
		}
	}
	return b.String()
}

// AnthropicBody 生成 Messages 请求体：
//   - system 消息合并为顶层 system；tool 消息转为 user 里的 tool_result 块；相邻同角色消息合并（协议要求交替）；
//   - assistant 的 tool_calls 转为 tool_use 块；带签名的历史思考内容转为 thinking 块原样回传；
//   - thinking=on 时发送 thinking.budget_tokens（max_tokens 的一半，至少 1024），并去掉 temperature / top_p；
//   - tool_choice：auto / none / required→any / 指定函数→tool；思考模式下强制调用按模型策略降级或拒绝。
func AnthropicBody(req *ChatRequest, m ModelConfig) (map[string]any, []string, *Error) {
	if gerr := validateRequest(req); gerr != nil {
		return nil, nil, gerr
	}
	var warnings []string
	thinking := m.Thinking == "on"
	maxTokens := req.MaxTokens
	if maxTokens > 0 && m.MaxOutputTokens > 0 && maxTokens > m.MaxOutputTokens {
		return nil, nil, newErr(http.StatusBadRequest, CodeBadRequest, "max_tokens 超过该模型上限 %d", m.MaxOutputTokens)
	}
	if maxTokens == 0 {
		maxTokens = m.MaxOutputTokens
	}
	if maxTokens == 0 {
		maxTokens = anthropicDefaultMaxTokens
	}
	body := map[string]any{"model": m.UpstreamModel, "max_tokens": maxTokens}
	if thinking {
		if maxTokens <= anthropicThinkingMin {
			return nil, nil, newErr(http.StatusBadRequest, CodeBadRequest, "思考模式下 max_tokens 必须大于 %d", anthropicThinkingMin)
		}
		budget := maxTokens / 2
		if budget < anthropicThinkingMin {
			budget = anthropicThinkingMin
		}
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	}

	var system []string
	type amsg struct {
		Role    string   `json:"role"`
		Content []aBlock `json:"content"`
	}
	var msgs []amsg
	push := func(role string, blocks []aBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
			return
		}
		msgs = append(msgs, amsg{Role: role, Content: blocks})
	}
	for _, mm := range req.Messages {
		switch mm.Role {
		case "system", "developer":
			if t := textOf(mm.Content); t != "" {
				system = append(system, t)
			}
		case "user":
			bs, gerr := contentBlocks(mm.Content)
			if gerr != nil {
				return nil, nil, gerr
			}
			push("user", bs)
		case "tool":
			push("user", []aBlock{{"type": "tool_result", "tool_use_id": mm.ToolCallID, "content": textOf(mm.Content)}})
		case "assistant":
			var bs []aBlock
			if mm.ReasoningContent != nil && *mm.ReasoningContent != "" && mm.ReasoningSignature != "" {
				bs = append(bs, aBlock{"type": "thinking", "thinking": *mm.ReasoningContent, "signature": mm.ReasoningSignature})
			}
			cb, gerr := contentBlocks(mm.Content)
			if gerr != nil {
				return nil, nil, gerr
			}
			bs = append(bs, cb...)
			for _, tc := range mm.ToolCalls {
				input := json.RawMessage(tc.Function.Arguments)
				if len(input) == 0 || !json.Valid(input) {
					input = json.RawMessage("{}")
				}
				bs = append(bs, aBlock{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input})
			}
			push("assistant", bs)
		default:
			return nil, nil, newErr(http.StatusBadRequest, CodeBadRequest, "不支持的消息角色 %q", mm.Role)
		}
	}
	if len(msgs) == 0 {
		return nil, nil, newErr(http.StatusBadRequest, CodeBadRequest, "除 system 外至少需要一条消息")
	}
	body["messages"] = msgs
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n\n")
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			var f struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			}
			_ = json.Unmarshal(t.Function, &f)
			schema := f.Parameters
			if len(schema) == 0 || string(schema) == "null" {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tool := map[string]any{"name": f.Name, "input_schema": schema}
			if f.Description != "" {
				tool["description"] = f.Description
			}
			tools = append(tools, tool)
		}
		body["tools"] = tools
	}
	if len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
		var s string
		var tc map[string]any
		if json.Unmarshal(req.ToolChoice, &s) == nil {
			switch s {
			case "auto", "":
				tc = map[string]any{"type": "auto"}
			case "none":
				tc = map[string]any{"type": "none"}
			case "required":
				tc = map[string]any{"type": "any"}
			default:
				return nil, nil, newErr(http.StatusBadRequest, CodeBadRequest, "tool_choice 不合法：%q", s)
			}
		} else {
			var f struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if json.Unmarshal(req.ToolChoice, &f) != nil || f.Function.Name == "" {
				return nil, nil, newErr(http.StatusBadRequest, CodeBadRequest, "tool_choice 不合法")
			}
			tc = map[string]any{"type": "tool", "name": f.Function.Name}
		}
		if thinking && (tc["type"] == "any" || tc["type"] == "tool") {
			if m.ToolChoicePolicy == "reject" {
				return nil, nil, newErr(http.StatusBadRequest, CodeToolChoice,
					"模型处于思考模式，不支持 tool_choice=required 或指定函数；请改用 auto，或使用关闭思考模式的模型")
			}
			tc = map[string]any{"type": "auto"}
			warnings = append(warnings, "tool_choice_downgraded: 思考模式不支持 required / 指定函数，已降级为 auto")
		}
		if len(req.Tools) > 0 {
			body["tool_choice"] = tc
		}
	}
	if thinking {
		if req.Temperature != nil || req.TopP != nil {
			warnings = append(warnings, "sampling_ignored: 思考模式下 Anthropic 不支持调整 temperature / top_p，已忽略")
		}
	} else {
		if req.Temperature != nil {
			body["temperature"] = *req.Temperature
		}
		if req.TopP != nil {
			body["top_p"] = *req.TopP
		}
	}
	if len(req.Stop) > 0 && string(req.Stop) != "null" {
		var one string
		var many []string
		if json.Unmarshal(req.Stop, &one) == nil {
			many = []string{one}
		} else if json.Unmarshal(req.Stop, &many) != nil {
			return nil, nil, newErr(http.StatusBadRequest, CodeBadRequest, "stop 只能是字符串或字符串数组")
		}
		body["stop_sequences"] = many
	}
	if req.Stream {
		body["stream"] = true
	}
	return body, warnings, nil
}

type aUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// toUsage Anthropic 的 input_tokens 不含缓存部分：prompt = input + 缓存读取 + 缓存写入，cached = 缓存读取。
func (a aUsage) toUsage() Usage {
	u := Usage{PromptTokens: a.InputTokens + a.CacheReadInputTokens + a.CacheCreationInputTokens,
		CompletionTokens: a.OutputTokens, CachedTokens: a.CacheReadInputTokens}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
	return u
}

type aContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

type aResponse struct {
	ID         string     `json:"id"`
	Model      string     `json:"model"`
	Content    []aContent `json:"content"`
	StopReason string     `json:"stop_reason"`
	Usage      *aUsage    `json:"usage"`
}

func stopReason(s string) string {
	switch s {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	}
	return s
}

func (c *Client) anthropicChat(ctx context.Context, req *ChatRequest, up Upstream, m ModelConfig) (*ChatResponse, *Error) {
	r := *req
	r.Stream = false
	body, warns, gerr := AnthropicBody(&r, m)
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
	var w aResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&w); err != nil {
		if ctx.Err() != nil {
			return nil, newErr(http.StatusGatewayTimeout, CodeUpstreamTimeout, "上游响应超时")
		}
		return nil, newErr(http.StatusBadGateway, CodeUpstreamProtocol, "上游返回的不是合法 JSON")
	}
	msg := Message{Role: "assistant"}
	var text, reasoning strings.Builder
	sawReasoning := false
	for _, b := range w.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "thinking":
			sawReasoning = true
			reasoning.WriteString(b.Thinking)
			msg.ReasoningSignature = b.Signature
		case "tool_use":
			args := string(b.Input)
			if args == "" || args == "null" {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: b.ID, Type: "function", Function: FunctionCall{Name: b.Name, Arguments: args}})
		}
	}
	if text.Len() > 0 || len(msg.ToolCalls) == 0 {
		msg.Content, _ = json.Marshal(text.String())
	} else {
		msg.Content = json.RawMessage("null")
	}
	if sawReasoning {
		s := reasoning.String()
		msg.ReasoningContent = &s
	}
	out := &ChatResponse{ID: w.ID, Model: w.Model, Message: msg, FinishReason: stopReason(w.StopReason), Warnings: warns}
	if w.Usage != nil {
		out.Usage = w.Usage.toUsage()
	} else {
		out.Usage = Estimate(req.Messages, msg)
	}
	return out, nil
}

// anthropicStream 解析 Messages SSE：message_start（输入用量）→ content_block_start / delta / stop
// （text_delta、thinking_delta、signature_delta、input_json_delta）→ message_delta（stop_reason、累计输出用量）→ message_stop。
// 流中的 error 事件（如 overloaded_error）按上游错误处理；超时与中断语义与 OpenAI 兼容适配器一致。
func (c *Client) anthropicStream(ctx context.Context, req *ChatRequest, up Upstream, m ModelConfig, onDelta func(Delta) error) (*ChatResponse, *Error) {
	r := *req
	r.Stream = true
	body, warns, gerr := AnthropicBody(&r, m)
	if gerr != nil {
		return nil, gerr
	}
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
	sawReasoning, sawStart, sawOutput := false, false, false
	signature := ""
	var usage aUsage
	type blk struct {
		typ  string
		tool int // tool_use 块对应的 tool_calls 下标
	}
	blocks := map[int]*blk{}
	calls := map[int]*ToolCall{}
	nTools := 0
	fail := func(e *Error) (*ChatResponse, *Error) {
		var u Usage
		if sawStart {
			u = usage.toUsage()
			if !sawOutput {
				part := Message{Role: "assistant"}
				part.Content, _ = json.Marshal(content.String() + reasoning.String())
				u.CompletionTokens = Estimate(nil, part).CompletionTokens
				u.TotalTokens = u.PromptTokens + u.CompletionTokens
				u.Estimated = true
			}
		} else {
			part := Message{Role: "assistant"}
			part.Content, _ = json.Marshal(content.String())
			u = Estimate(req.Messages, part)
		}
		e.Usage = &u
		return nil, e
	}
	emit := func(d Delta) *Error {
		if err := onDelta(d); err != nil {
			return newErr(499, "client_closed", "调用方已断开")
		}
		return nil
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	done := false
	for sc.Scan() {
		timer.Reset(idle)
		line := strings.TrimRight(sc.Text(), "\r")
		if !strings.HasPrefix(line, "data:") {
			continue // event: 行与 data 里的 type 重复，按 data 解析
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev struct {
			Type    string          `json:"type"`
			Index   int             `json:"index"`
			Message *aResponse      `json:"message"`
			Block   *aContent       `json:"content_block"`
			Delta   json.RawMessage `json:"delta"`
			Usage   *aUsage         `json:"usage"`
			Error   json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return fail(newErr(http.StatusBadGateway, CodeUpstreamProtocol, "上游流式数据无法解析"))
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				out.ID, out.Model = ev.Message.ID, ev.Message.Model
				if ev.Message.Usage != nil {
					usage = *ev.Message.Usage
					sawStart = true
				}
			}
		case "content_block_start":
			if ev.Block == nil {
				continue
			}
			b := &blk{typ: ev.Block.Type}
			blocks[ev.Index] = b
			switch ev.Block.Type {
			case "tool_use":
				b.tool = nTools
				nTools++
				i := b.tool
				calls[i] = &ToolCall{ID: ev.Block.ID, Type: "function", Function: FunctionCall{Name: ev.Block.Name}}
				if e := emit(Delta{ToolCalls: []ToolCall{{Index: &i, ID: ev.Block.ID, Type: "function",
					Function: FunctionCall{Name: ev.Block.Name}}}}); e != nil {
					return fail(e)
				}
			case "thinking":
				sawReasoning = true
			}
		case "content_block_delta":
			var d struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
			}
			_ = json.Unmarshal(ev.Delta, &d)
			var e *Error
			switch d.Type {
			case "text_delta":
				content.WriteString(d.Text)
				e = emit(Delta{Content: d.Text})
			case "thinking_delta":
				sawReasoning = true
				reasoning.WriteString(d.Thinking)
				e = emit(Delta{ReasoningContent: d.Thinking})
			case "signature_delta":
				signature = d.Signature
			case "input_json_delta":
				if b := blocks[ev.Index]; b != nil && b.typ == "tool_use" {
					i := b.tool
					calls[i].Function.Arguments += d.PartialJSON
					if d.PartialJSON != "" {
						e = emit(Delta{ToolCalls: []ToolCall{{Index: &i, Function: FunctionCall{Arguments: d.PartialJSON}}}})
					}
				}
			}
			if e != nil {
				return fail(e)
			}
		case "message_delta":
			var d struct {
				StopReason string `json:"stop_reason"`
			}
			_ = json.Unmarshal(ev.Delta, &d)
			if ev.Usage != nil {
				sawOutput = true
				usage.OutputTokens = ev.Usage.OutputTokens
				if ev.Usage.InputTokens > 0 {
					usage.InputTokens = ev.Usage.InputTokens
				}
			}
			if d.StopReason != "" {
				out.FinishReason = stopReason(d.StopReason)
				if e := emit(Delta{FinishReason: out.FinishReason}); e != nil {
					return fail(e)
				}
			}
		case "message_stop":
			done = true
		case "error":
			e := anthropicStreamError(ev.Error, up.APIKey)
			return fail(e)
		}
		if done {
			break
		}
	}
	if err := sc.Err(); err != nil || !done {
		if timedOut.Load() {
			return fail(newErr(http.StatusGatewayTimeout, CodeUpstreamTimeout, "上游流式响应超时（%s 内没有收到数据）", idle))
		}
		if ctx.Err() != nil {
			return fail(newErr(499, "client_closed", "调用方已断开"))
		}
		if err != nil {
			return fail(newErr(http.StatusBadGateway, CodeUpstreamUnavailable, "上游流中断"))
		}
		return fail(newErr(http.StatusBadGateway, CodeUpstreamProtocol, "上游流未正常结束"))
	}
	msg := Message{Role: "assistant", ReasoningSignature: signature}
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
		tc := *calls[i]
		if tc.Function.Arguments == "" {
			tc.Function.Arguments = "{}"
		}
		if !json.Valid([]byte(tc.Function.Arguments)) {
			out.Warnings = append(out.Warnings, "tool_arguments_invalid_json: "+tc.Function.Name)
		}
		msg.ToolCalls = append(msg.ToolCalls, tc)
	}
	out.Message = msg
	if sawStart {
		out.Usage = usage.toUsage()
		if !sawOutput {
			out.Usage.Estimated = true
			out.Usage.CompletionTokens = Estimate(nil, msg).CompletionTokens
			out.Usage.TotalTokens = out.Usage.PromptTokens + out.Usage.CompletionTokens
		}
	} else {
		out.Usage = Estimate(req.Messages, msg)
	}
	return out, nil
}

// anthropicStreamError 流内 error 事件：overloaded_error / api_error 视为上游异常（HTTP 已是 200，不再重试）。
func anthropicStreamError(raw json.RawMessage, key string) *Error {
	var e struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &e)
	status := http.StatusBadRequest
	switch e.Type {
	case "overloaded_error":
		status = 529
	case "api_error":
		status = http.StatusInternalServerError
	case "rate_limit_error":
		status = http.StatusTooManyRequests
	}
	b, _ := json.Marshal(map[string]any{"error": e})
	ge := upstreamError(status, b, key)
	ge.Retryable = false
	return ge
}
