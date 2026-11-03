package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/gateway/mockupstream"
)

const testKey = "sk-test-0123456789abcdefWXYZ"

func up(m *mockupstream.Server, key string) gateway.Upstream {
	return gateway.Upstream{Name: "ds", BaseURL: m.URL, APIKey: key, Dialect: "deepseek", Timeout: 2 * time.Second}
}

func user(s string) gateway.Message {
	b, _ := json.Marshal(s)
	return gateway.Message{Role: "user", Content: b}
}

var tools = []gateway.Tool{{Type: "function", Function: json.RawMessage(`{"name":"get_ticket","parameters":{"type":"object","properties":{"id":{"type":"string"}}}}`)}}

func TestKeyring(t *testing.T) {
	mk := strings.Repeat("ab", 32)
	k, err := gateway.LoadKeyring("", mk)
	if err != nil || k == nil {
		t.Fatal(err)
	}
	ct, err := k.Seal("ds", testKey)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ct), "0123456789") {
		t.Fatal("密文里出现明文")
	}
	pt, err := k.Open("ds", ct)
	if err != nil || pt != testKey {
		t.Fatalf("roundtrip: %v", err)
	}
	if _, err := k.Open("other", ct); err == nil {
		t.Fatal("AAD 不同应解密失败")
	}
	ct[len(ct)-1] ^= 1
	if _, err := k.Open("ds", ct); err == nil {
		t.Fatal("篡改应解密失败")
	}
	var nilK *gateway.Keyring
	if _, err := nilK.Seal("ds", "x"); !errors.Is(err, gateway.ErrNoMasterKey) {
		t.Fatal("未配置主密钥应拒绝")
	}
	if _, err := gateway.ParseMasterKey("short"); err == nil {
		t.Fatal("短密钥应拒绝")
	}
	if k, _ := gateway.LoadKeyring("", ""); k != nil {
		t.Fatal("未配置应返回 nil")
	}
}

func TestRedact(t *testing.T) {
	in := "Authentication Fails, Your api key: ****WXYZ is invalid; Bearer abcdefghij; sk-live-abcdef123456; " + testKey
	out := gateway.Redact(in, testKey)
	for _, bad := range []string{"WXYZ", "abcdefghij", "sk-live", testKey} {
		if strings.Contains(out, bad) {
			t.Fatalf("未脱敏 %q: %s", bad, out)
		}
	}
}

func TestBuildBodyDeepSeek(t *testing.T) {
	u := gateway.Upstream{Dialect: "deepseek"}
	req := &gateway.ChatRequest{Messages: []gateway.Message{user("hi")}, Tools: tools, ToolChoice: json.RawMessage(`"required"`), Stream: true}
	body, warns, err := gateway.BuildBody(req, u, gateway.ModelConfig{UpstreamModel: "deepseek-flash", Thinking: "on", ToolChoicePolicy: "downgrade"})
	if err != nil {
		t.Fatal(err)
	}
	if string(body["tool_choice"].(json.RawMessage)) != `"auto"` || len(warns) != 1 {
		t.Fatalf("应降级为 auto: %v %v", body["tool_choice"], warns)
	}
	if body["thinking"].(map[string]string)["type"] != "enabled" {
		t.Fatal("应显式开启思考")
	}
	if so := body["stream_options"].(map[string]bool); !so["include_usage"] {
		t.Fatal("流式应带 include_usage")
	}
	req.ToolChoice = json.RawMessage(`{"type":"function","function":{"name":"get_ticket"}}`)
	if _, _, err := gateway.BuildBody(req, u, gateway.ModelConfig{Thinking: "on", ToolChoicePolicy: "reject"}); err == nil || err.Code != gateway.CodeToolChoice {
		t.Fatalf("reject 策略应报 tool_choice_unsupported: %v", err)
	}
	// 非思考模式 required 原样透传
	req.ToolChoice = json.RawMessage(`"required"`)
	body, _, _ = gateway.BuildBody(req, u, gateway.ModelConfig{Thinking: "off"})
	if string(body["tool_choice"].(json.RawMessage)) != `"required"` || body["thinking"].(map[string]string)["type"] != "disabled" {
		t.Fatal("非思考模式应保留 required 并显式关闭思考")
	}
	// 函数名预校验
	bad := &gateway.ChatRequest{Messages: []gateway.Message{user("hi")}, Tools: []gateway.Tool{{Type: "function", Function: json.RawMessage(`{"name":"kb.search"}`)}}}
	if _, _, err := gateway.BuildBody(bad, u, gateway.ModelConfig{}); err == nil || err.Code != gateway.CodeInvalidFunctionName {
		t.Fatal("函数名含 . 应拒绝")
	}
	// generic 方言去掉 reasoning_content
	rc := "x"
	r2 := &gateway.ChatRequest{Messages: []gateway.Message{{Role: "assistant", Content: json.RawMessage(`"a"`), ReasoningContent: &rc}}}
	body, _, _ = gateway.BuildBody(r2, gateway.Upstream{Dialect: "generic"}, gateway.ModelConfig{})
	if body["messages"].([]gateway.Message)[0].ReasoningContent != nil {
		t.Fatal("generic 方言不应回传 reasoning_content")
	}
	if r2.Messages[0].ReasoningContent == nil {
		t.Fatal("不应修改调用方的消息")
	}
}

func TestChatNonStreamAndMultiTurnTools(t *testing.T) {
	m := mockupstream.New(testKey)
	defer m.Close()
	c := &gateway.Client{}
	cfg := gateway.ModelConfig{UpstreamModel: "deepseek-flash", Thinking: "on", ToolChoicePolicy: "downgrade"}
	req := &gateway.ChatRequest{Messages: []gateway.Message{user("查一下工单 T-1")}, Tools: tools, ToolChoice: json.RawMessage(`"required"`)}
	res, err := c.Chat(context.Background(), req, up(m, testKey), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinishReason != "tool_calls" || len(res.Message.ToolCalls) != 1 || res.Message.ReasoningContent == nil {
		t.Fatalf("unexpected: %+v", res)
	}
	if res.Usage.PromptTokens != 12 || res.Usage.ReasoningTokens != 5 || res.Usage.Estimated {
		t.Fatalf("usage: %+v", res.Usage)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("降级应有 warning")
	}
	// 第二轮：原样回传 assistant 消息（含 reasoning_content），mock 缺失时会 400。
	req.Messages = append(req.Messages, res.Message, gateway.Message{Role: "tool", ToolCallID: res.Message.ToolCalls[0].ID, Content: json.RawMessage(`"{\"status\":\"处理中\"}"`)})
	req.ToolChoice = nil
	res2, err := c.Chat(context.Background(), req, up(m, testKey), cfg)
	if err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}
	if !strings.Contains(string(res2.Message.Content), "处理中") {
		t.Fatalf("第二轮内容: %s", res2.Message.Content)
	}
	msgs := m.LastRequest()["messages"].([]any)
	if msgs[1].(map[string]any)["reasoning_content"] == nil {
		t.Fatal("没有回传 reasoning_content")
	}
	// 去掉 reasoning_content 时 mock 按文档严格行为返回 400，网关给出明确错误
	req.Messages[1].ReasoningContent = nil
	if _, err := c.Chat(context.Background(), req, up(m, testKey), cfg); err == nil || err.Code != gateway.CodeUpstreamBadRequest {
		t.Fatalf("缺 reasoning_content 应 400: %v", err)
	}
}

func TestStreamAggregatesToolCallsAndUsage(t *testing.T) {
	m := mockupstream.New(testKey)
	defer m.Close()
	c := &gateway.Client{}
	req := &gateway.ChatRequest{Messages: []gateway.Message{user("查工单")}, Tools: tools}
	var reasoning, args strings.Builder
	finish := ""
	res, err := c.Stream(context.Background(), req, up(m, testKey), gateway.ModelConfig{UpstreamModel: "deepseek-v4-pro", Thinking: "on"}, func(d gateway.Delta) error {
		reasoning.WriteString(d.ReasoningContent)
		for _, tc := range d.ToolCalls {
			args.WriteString(tc.Function.Arguments)
		}
		if d.FinishReason != "" {
			finish = d.FinishReason
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if finish != "tool_calls" || args.String() != `{"id": "T-1"}` || reasoning.Len() == 0 {
		t.Fatalf("deltas: finish=%s args=%s", finish, args.String())
	}
	tc := res.Message.ToolCalls
	if len(tc) != 1 || tc[0].ID != "call_00_mock" || tc[0].Function.Name != "get_ticket" || tc[0].Function.Arguments != `{"id": "T-1"}` {
		t.Fatalf("aggregate: %+v", tc)
	}
	if res.Usage.TotalTokens != 19 || res.Usage.Estimated {
		t.Fatalf("usage 应取自最后一个带 finish_reason 的 chunk: %+v", res.Usage)
	}
	if string(res.Message.Content) != "null" {
		t.Fatalf("只有工具调用时 content 应为 null: %s", res.Message.Content)
	}
	if so, _ := m.LastRequest()["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Fatal("应带 include_usage")
	}
	// 无 usage 时估算
	m.OmitUsage = true
	res, err = c.Stream(context.Background(), &gateway.ChatRequest{Messages: []gateway.Message{user("你好")}}, up(m, testKey),
		gateway.ModelConfig{UpstreamModel: "deepseek-flash", Thinking: "off"}, func(gateway.Delta) error { return nil })
	if err != nil || !res.Usage.Estimated || res.Usage.CompletionTokens == 0 || !strings.Contains(string(res.Message.Content), "模拟模型") {
		t.Fatalf("estimate: %+v %v", res, err)
	}
}

func TestUpstreamErrors(t *testing.T) {
	m := mockupstream.New(testKey)
	defer m.Close()
	c := &gateway.Client{}
	cfg := gateway.ModelConfig{UpstreamModel: "deepseek-flash", Thinking: "off"}
	req := &gateway.ChatRequest{Messages: []gateway.Message{user("hi")}}
	wrong := "sk-wrong-key-zzzz9876"
	_, err := c.Chat(context.Background(), req, up(m, wrong), cfg)
	if err == nil || err.Code != gateway.CodeUpstreamAuth || err.Status != 502 {
		t.Fatalf("401: %v", err)
	}
	if strings.Contains(err.Message, "9876") || strings.Contains(err.Message, wrong) {
		t.Fatalf("401 消息未脱敏: %s", err.Message)
	}
	// provider_default（默认思考）+ 指定函数：上游 400 也映射为明确的 tool_choice 错误
	_, err = c.Chat(context.Background(), &gateway.ChatRequest{Messages: req.Messages, Tools: tools,
		ToolChoice: json.RawMessage(`"required"`)}, gateway.Upstream{Name: "g", BaseURL: m.URL, APIKey: testKey, Dialect: "generic"}, cfg)
	if err == nil || err.Code != gateway.CodeToolChoice {
		t.Fatalf("tool_choice 400: %v", err)
	}
	m.FailStatus = 503
	if _, err := c.Chat(context.Background(), req, up(m, testKey), cfg); err == nil || err.Code != gateway.CodeUpstreamUnavailable {
		t.Fatalf("5xx: %v", err)
	}
	m.FailStatus = 0
	m.Delay = 3 * time.Second
	u := up(m, testKey)
	u.Timeout = 200 * time.Millisecond
	if _, err := c.Chat(context.Background(), req, u, cfg); err == nil || err.Code != gateway.CodeUpstreamTimeout {
		t.Fatalf("timeout: %v", err)
	}
	if _, err := c.Stream(context.Background(), req, u, cfg, func(gateway.Delta) error { return nil }); err == nil || err.Code != gateway.CodeUpstreamTimeout {
		t.Fatalf("stream timeout: %v", err)
	}
}
