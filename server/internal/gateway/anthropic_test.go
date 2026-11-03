package gateway_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/gateway"
)

func TestAnthropicBodyMapping(t *testing.T) {
	r := "想一想"
	req := &gateway.ChatRequest{
		Messages: []gateway.Message{
			{Role: "system", Content: json.RawMessage(`"你是助手"`)},
			user("北京天气？"),
			{Role: "assistant", ReasoningContent: &r, ReasoningSignature: "sig", ToolCalls: []gateway.ToolCall{
				{ID: "call_1", Type: "function", Function: gateway.FunctionCall{Name: "weather", Arguments: `{"city":"北京"}`}}}},
			{Role: "tool", ToolCallID: "call_1", Content: json.RawMessage(`"晴"`)},
		},
		Tools:      []gateway.Tool{{Type: "function", Function: json.RawMessage(`{"name":"weather","parameters":{"type":"object"}}`)}},
		ToolChoice: json.RawMessage(`"required"`),
		Stop:       json.RawMessage(`"END"`),
	}
	body, _, err := gateway.AnthropicBody(req, gateway.ModelConfig{UpstreamModel: "claude", Thinking: "off"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(body)
	s := string(b)
	for _, want := range []string{`"system":"你是助手"`, `"max_tokens":4096`, `"type":"tool_use"`, `"input":{"city":"北京"}`,
		`"type":"tool_result"`, `"tool_use_id":"call_1"`, `"input_schema":{"type":"object"}`, `"tool_choice":{"type":"any"}`,
		`"stop_sequences":["END"]`, `"type":"thinking"`, `"signature":"sig"`} {
		if !strings.Contains(s, want) {
			t.Errorf("缺少 %s：%s", want, s)
		}
	}
	if strings.Contains(s, `"role":"system"`) || strings.Contains(s, `"role":"tool"`) {
		t.Errorf("system/tool 角色应转换：%s", s)
	}
}

func TestAnthropicBodyThinking(t *testing.T) {
	tp := 0.5
	req := &gateway.ChatRequest{Messages: []gateway.Message{user("hi")}, MaxTokens: 8000, Temperature: &tp}
	body, warns, err := gateway.AnthropicBody(req, gateway.ModelConfig{UpstreamModel: "claude", Thinking: "on"})
	if err != nil {
		t.Fatal(err)
	}
	th, _ := body["thinking"].(map[string]any)
	if th["type"] != "enabled" || th["budget_tokens"] != 4000 || body["temperature"] != nil || len(warns) == 0 {
		t.Fatalf("body=%v warns=%v", body, warns)
	}
	req.MaxTokens = 1000
	if _, _, err := gateway.AnthropicBody(req, gateway.ModelConfig{UpstreamModel: "claude", Thinking: "on"}); err == nil {
		t.Fatal("max_tokens ≤ 1024 时开启思考应报错")
	}
	// 无签名的思考内容不回传（上游会拒绝）
	r := "x"
	req = &gateway.ChatRequest{Messages: []gateway.Message{user("hi"), {Role: "assistant", ReasoningContent: &r, Content: json.RawMessage(`"ok"`)}, user("再说")}}
	body, _, _ = gateway.AnthropicBody(req, gateway.ModelConfig{UpstreamModel: "claude", Thinking: "off"})
	if b, _ := json.Marshal(body); strings.Contains(string(b), `"thinking"`) {
		t.Fatalf("%s", b)
	}
}
