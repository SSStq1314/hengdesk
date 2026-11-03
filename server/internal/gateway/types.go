// Package gateway 模型网关核心：OpenAI 兼容适配器、上游密钥加密、DeepSeek 方言处理与脱敏。
// 实现只用标准库（D-24 原计划 go-openai；为了原样保存并回传 reasoning_content、
// 精确控制 SSE 解析和错误脱敏，改为自写最小客户端，见 docs/progress/m1-block2.md）。
package gateway

import "encoding/json"

// Message 对话消息。字段与 OpenAI Chat Completions 一致，另含 DeepSeek 的 reasoning_content。
// Content 用 RawMessage 原样透传（字符串、null 或多模态数组）。
type Message struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content,omitempty"`
	ReasoningContent *string         `json:"reasoning_content,omitempty"`
	Name             string          `json:"name,omitempty"`
	ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
}

// ToolCall 工具调用。
type ToolCall struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

// FunctionCall 函数名与 JSON 字符串参数。
type FunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// Tool 工具定义（只支持 function）。
type Tool struct {
	Type     string          `json:"type"`
	Function json.RawMessage `json:"function"`
}

// ChatRequest 网关内部统一请求（也是 /api/v1/chat/completions 的请求体）。
type ChatRequest struct {
	Model       string          `json:"model"`
	Messages    []Message       `json:"messages"`
	Tools       []Tool          `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        json.RawMessage `json:"stop,omitempty"`
}

// Usage token 用量。Estimated 为 true 表示上游未返回，由网关估算。
type Usage struct {
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	TotalTokens      int  `json:"total_tokens"`
	CachedTokens     int  `json:"cached_tokens,omitempty"`
	ReasoningTokens  int  `json:"reasoning_tokens,omitempty"`
	Estimated        bool `json:"estimated,omitempty"`
}

// ChatResponse 非流式结果（流式结束时也聚合成同样结构，用于审计和计量）。
type ChatResponse struct {
	ID           string   `json:"id"`
	Model        string   `json:"model"`
	Message      Message  `json:"message"`
	FinishReason string   `json:"finish_reason"`
	Usage        Usage    `json:"usage"`
	Warnings     []string `json:"warnings,omitempty"`
}

// Delta 流式增量。
type Delta struct {
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	FinishReason     string     `json:"finish_reason,omitempty"`
}

// 上游线格式（只解析需要的字段，其余忽略）。
type wireUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
}

func (w *wireUsage) toUsage() Usage {
	u := Usage{PromptTokens: w.PromptTokens, CompletionTokens: w.CompletionTokens, TotalTokens: w.TotalTokens}
	if w.PromptTokensDetails != nil {
		u.CachedTokens = w.PromptTokensDetails.CachedTokens
	}
	if u.CachedTokens == 0 {
		u.CachedTokens = w.PromptCacheHitTokens
	}
	if w.CompletionTokensDetails != nil {
		u.ReasoningTokens = w.CompletionTokensDetails.ReasoningTokens
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u
}

type wireMessage struct {
	Role             string     `json:"role"`
	Content          *string    `json:"content"`
	ReasoningContent *string    `json:"reasoning_content"`
	ToolCalls        []ToolCall `json:"tool_calls"`
}

type wireChoice struct {
	Index        int          `json:"index"`
	Message      *wireMessage `json:"message"`
	Delta        *wireMessage `json:"delta"`
	FinishReason *string      `json:"finish_reason"`
}

type wireResponse struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []wireChoice `json:"choices"`
	Usage   *wireUsage   `json:"usage"`
}
