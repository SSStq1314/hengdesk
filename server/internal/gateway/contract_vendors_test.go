package gateway_test

// 通义（DashScope OpenAI 兼容模式）、vLLM、Anthropic 录制回放契约测试（D-35）。
// 回放带由 tests/spikes/model-compat/make-doc-cassettes.py 按各厂商公开文档构造，
// 标注「文档构造，待真实录制替换」；拿到凭据后用 cmd/cassette-record 真实录制覆盖同名文件。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/gateway"
)

type vendorCase struct {
	cassette
	Events []struct {
		Event string          `json:"event"`
		Data  json.RawMessage `json:"data"`
	} `json:"events"`
}

type vendorSpec struct {
	file, kind, dialect, path, model string
	thinking                         func(rb map[string]any) string
}

var vendors = []vendorSpec{
	{"dashscope", "openai", "dashscope", "/chat/completions", "qwen-plus", func(rb map[string]any) string {
		if rb["enable_thinking"] == true {
			return "on"
		}
		return "off"
	}},
	{"vllm", "openai", "vllm", "/chat/completions", "Qwen/Qwen3-8B", func(rb map[string]any) string {
		if k, _ := rb["chat_template_kwargs"].(map[string]any); k["enable_thinking"] == true {
			return "on"
		}
		return "off"
	}},
	{"anthropic", gateway.KindAnthropic, "", "/v1/messages", "claude-sonnet-4-5", func(rb map[string]any) string {
		if _, ok := rb["thinking"]; ok {
			return "on"
		}
		return "off"
	}},
}

func loadVendor(t *testing.T, name string) map[string]vendorCase {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + "_cassette.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Note  string                `json:"_note"`
		Cases map[string]vendorCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.Note == "" {
		t.Fatal("回放带必须注明来源（_note）")
	}
	if bytes.Contains(b, []byte(testKey)) || bytes.Contains(bytes.ToLower(b), []byte(`"authorization":`)) || bytes.Contains(b, []byte(`"x-api-key":`)) {
		t.Fatal("回放带不得含请求头或密钥")
	}
	return f.Cases
}

func vendorReplay(t *testing.T, v vendorSpec, cs vendorCase, delay time.Duration) (*httptest.Server, *map[string]any) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authOK := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
		if v.kind == gateway.KindAnthropic {
			authOK = r.Header.Get("x-api-key") != "" && r.Header.Get("anthropic-version") == gateway.AnthropicVersion && r.Header.Get("Authorization") == ""
		}
		if r.URL.Path != v.path || !authOK {
			http.Error(w, "bad request path/auth: "+r.URL.Path, http.StatusTeapot)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		fl := w.(http.Flusher)
		switch {
		case cs.Events != nil:
			w.Header().Set("Content-Type", "text/event-stream")
			for _, e := range cs.Events {
				var buf bytes.Buffer
				_ = json.Compact(&buf, e.Data)
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Event, buf.Bytes())
				fl.Flush()
			}
		case cs.Chunks != nil:
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ch := range cs.Chunks {
				var buf bytes.Buffer
				_ = json.Compact(&buf, ch)
				_, _ = fmt.Fprintf(w, "data: %s\n\n", buf.Bytes())
				fl.Flush()
			}
			if cs.DoneMarker {
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			}
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(cs.Status)
			_, _ = w.Write(cs.Body)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func (v vendorSpec) up(srv *httptest.Server) gateway.Upstream {
	return gateway.Upstream{Name: v.file, Kind: v.kind, BaseURL: srv.URL, APIKey: testKey, Dialect: v.dialect, Timeout: 5 * time.Second}
}

func checkVendorRequest(t *testing.T, v vendorSpec, want, got map[string]any) {
	t.Helper()
	for _, k := range []string{"model", "stream"} {
		if fmt.Sprint(want[k]) != fmt.Sprint(got[k]) {
			t.Errorf("请求字段 %s：录制 %v，网关发出 %v", k, want[k], got[k])
		}
	}
	if v.thinking(want) != v.thinking(got) {
		t.Errorf("思考开关：录制 %v，网关发出 %v", want, got)
	}
	if v.kind == gateway.KindAnthropic {
		if got["max_tokens"] == nil {
			t.Error("Anthropic 请求必须带 max_tokens")
		}
		if th, _ := got["thinking"].(map[string]any); th != nil && th["budget_tokens"] == nil {
			t.Error("thinking 必须带 budget_tokens")
		}
	} else if want["stream"] == true {
		if so, _ := got["stream_options"].(map[string]any); so["include_usage"] != true {
			t.Error("流式请求应带 stream_options.include_usage")
		}
	}
}

func TestVendorContractNonStream(t *testing.T) {
	for _, v := range vendors {
		all := loadVendor(t, v.file)
		for _, name := range []string{"chat-thinking", "chat-no-thinking"} {
			t.Run(v.file+"/"+name, func(t *testing.T) {
				cs := all[name]
				srv, got := vendorReplay(t, v, cs, 0)
				th := v.thinking(cs.RequestBody)
				res, err := testClient().Chat(context.Background(),
					&gateway.ChatRequest{Messages: []gateway.Message{user("用一句话介绍你自己。")}},
					v.up(srv), gateway.ModelConfig{UpstreamModel: v.model, Thinking: th})
				if err != nil {
					t.Fatal(err)
				}
				checkVendorRequest(t, v, cs.RequestBody, *got)
				if res.FinishReason != "stop" || len(res.Message.Content) < 3 || res.Usage.Estimated || res.Usage.TotalTokens == 0 {
					t.Fatalf("res %+v", res)
				}
				hasR := res.Message.ReasoningContent != nil && *res.Message.ReasoningContent != ""
				if hasR != (th == "on") {
					t.Fatalf("思考内容透传不符：%v", hasR)
				}
				if v.kind == gateway.KindAnthropic {
					if res.Usage.PromptTokens != 25 || res.Usage.CachedTokens != 5 || res.Usage.TotalTokens != 65 {
						t.Fatalf("usage 换算：%+v", res.Usage)
					}
					if th == "on" && res.Message.ReasoningSignature != "doc-signature" {
						t.Fatal("应保留 thinking signature 以便多轮回传")
					}
				}
			})
		}
	}
}

func TestVendorContractStream(t *testing.T) {
	for _, v := range vendors {
		all := loadVendor(t, v.file)
		for _, name := range []string{"stream-thinking", "stream-no-thinking"} {
			t.Run(v.file+"/"+name, func(t *testing.T) {
				cs := all[name]
				srv, got := vendorReplay(t, v, cs, 0)
				th := v.thinking(cs.RequestBody)
				var content, reasoning strings.Builder
				finish := ""
				res, err := testClient().Stream(context.Background(),
					&gateway.ChatRequest{Messages: []gateway.Message{user("用一句话介绍你自己。")}, Stream: true},
					v.up(srv), gateway.ModelConfig{UpstreamModel: v.model, Thinking: th},
					func(d gateway.Delta) error {
						content.WriteString(d.Content)
						reasoning.WriteString(d.ReasoningContent)
						if d.FinishReason != "" {
							finish = d.FinishReason
						}
						return nil
					})
				if err != nil {
					t.Fatal(err)
				}
				checkVendorRequest(t, v, cs.RequestBody, *got)
				if finish != cs.FinishReason || res.FinishReason != cs.FinishReason || content.Len() == 0 {
					t.Fatalf("finish=%q content=%q", finish, content.String())
				}
				want := int(cs.Usage["total_tokens"].(float64))
				if res.Usage.Estimated || res.Usage.TotalTokens != want {
					t.Fatalf("usage：%+v，期望 total=%d", res.Usage, want)
				}
				if (reasoning.Len() > 0) != (th == "on") {
					t.Fatalf("流式思考内容透传不符：%q", reasoning.String())
				}
			})
		}
	}
}

func TestVendorContractErrors(t *testing.T) {
	for _, v := range vendors {
		all := loadVendor(t, v.file)
		cases := []struct {
			name, code string
			status     int
			retry      bool
		}{
			{"err-invalid-key", gateway.CodeUpstreamAuth, 502, false},
			{"err-invalid-model", gateway.CodeUpstreamBadRequest, 400, false},
			{"err-context-too-long", gateway.CodeUpstreamBadRequest, 400, false},
		}
		if v.kind == gateway.KindAnthropic {
			cases = append(cases, struct {
				name, code string
				status     int
				retry      bool
			}{"err-overloaded", gateway.CodeUpstreamUnavailable, 502, true})
		}
		for _, tc := range cases {
			t.Run(v.file+"/"+tc.name, func(t *testing.T) {
				srv, _ := vendorReplay(t, v, all[tc.name], 0)
				_, err := testClient().Chat(context.Background(),
					&gateway.ChatRequest{Messages: []gateway.Message{user("hi")}}, v.up(srv),
					gateway.ModelConfig{UpstreamModel: v.model, Thinking: "off"})
				if err == nil || err.Code != tc.code || err.Status != tc.status || err.Retryable != tc.retry {
					t.Fatalf("err %+v", err)
				}
				if strings.Contains(err.Message, testKey) {
					t.Fatalf("错误消息未脱敏：%s", err.Message)
				}
			})
		}
	}
	// Anthropic 流内 error 事件：首字节已发出，不可重试，且保留已产生的部分用量。
	v := vendors[2]
	srv, _ := vendorReplay(t, v, loadVendor(t, v.file)["stream-overloaded"], 0)
	_, err := testClient().Stream(context.Background(),
		&gateway.ChatRequest{Messages: []gateway.Message{user("hi")}, Stream: true}, v.up(srv),
		gateway.ModelConfig{UpstreamModel: v.model, Thinking: "off"}, func(gateway.Delta) error { return nil })
	if err == nil || err.Retryable || gateway.ShouldRetry(err, true) {
		t.Fatalf("流内错误：%+v", err)
	}
}

func TestVendorContractTimeout(t *testing.T) {
	for _, v := range vendors {
		all := loadVendor(t, v.file)
		for _, name := range []string{"chat-no-thinking", "stream-no-thinking"} {
			t.Run(v.file+"/"+name, func(t *testing.T) {
				cs := all[name]
				srv, _ := vendorReplay(t, v, cs, 3*time.Second)
				u := v.up(srv)
				u.Timeout = 200 * time.Millisecond
				req := &gateway.ChatRequest{Messages: []gateway.Message{user("hi")}}
				cfg := gateway.ModelConfig{UpstreamModel: v.model, Thinking: "off"}
				var err *gateway.Error
				if strings.HasPrefix(name, "chat") {
					_, err = testClient().Chat(context.Background(), req, u, cfg)
				} else {
					req.Stream = true
					_, err = testClient().Stream(context.Background(), req, u, cfg, func(gateway.Delta) error { return nil })
				}
				if err == nil || err.Code != gateway.CodeUpstreamTimeout || err.Retryable {
					t.Fatalf("timeout（超时不重试）：%+v", err)
				}
			})
		}
	}
}
