package gateway_test

// DeepSeek 录制回放契约测试（D-35）：回放带由 tests/spikes/model-compat/make-cassette.py
// 从真实调用记录生成，只含请求体和响应体（无任何请求头 / 密钥）。CI 中离线运行。

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

type cassette struct {
	SourceCase   string            `json:"source_case"`
	Status       int               `json:"status"`
	RequestBody  map[string]any    `json:"request_body"`
	Body         json.RawMessage   `json:"body"`
	Chunks       []json.RawMessage `json:"chunks"`
	DoneMarker   bool              `json:"done_marker"`
	Usage        map[string]any    `json:"usage"`
	FinishReason string            `json:"finish_reason"`
}

func loadCassettes(t *testing.T) map[string]cassette {
	t.Helper()
	b, err := os.ReadFile("testdata/deepseek_cassette.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]cassette
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// replay 回放一条录制；同时校验网关发出的请求与录制请求在关键字段上一致。
func replay(t *testing.T, cs cassette, delay time.Duration) (*httptest.Server, *map[string]any) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "bad request path/auth", 400)
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
		if cs.Chunks == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(cs.Status)
			_, _ = w.Write(cs.Body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, ch := range cs.Chunks {
			var buf bytes.Buffer
			_ = json.Compact(&buf, ch) // 回放带是缩进格式，SSE 每条 data 必须单行
			_, _ = fmt.Fprintf(w, "data: %s\n\n", buf.Bytes())
			fl.Flush()
		}
		if cs.DoneMarker {
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func dsUp(srv *httptest.Server) gateway.Upstream {
	return gateway.Upstream{Name: "deepseek", BaseURL: srv.URL, APIKey: testKey, Dialect: "deepseek", Timeout: 5 * time.Second}
}

func thinkingOf(rb map[string]any) string {
	if th, ok := rb["thinking"].(map[string]any); ok && th["type"] == "enabled" {
		return "on"
	}
	return "off"
}

func checkRequest(t *testing.T, want, got map[string]any) {
	t.Helper()
	for _, k := range []string{"model", "stream"} {
		if fmt.Sprint(want[k]) != fmt.Sprint(got[k]) {
			t.Errorf("请求字段 %s：录制 %v，网关发出 %v", k, want[k], got[k])
		}
	}
	if wt, gt := want["thinking"], got["thinking"]; fmt.Sprint(wt) != fmt.Sprint(gt) {
		t.Errorf("thinking：录制 %v，网关发出 %v", wt, gt)
	}
	if want["stream"] == true {
		if so, _ := got["stream_options"].(map[string]any); so["include_usage"] != true {
			t.Error("流式请求应带 stream_options.include_usage")
		}
	}
}

func TestDeepSeekContractNonStream(t *testing.T) {
	all := loadCassettes(t)
	for _, name := range []string{"chat-thinking", "chat-no-thinking"} {
		t.Run(name, func(t *testing.T) {
			cs := all[name]
			srv, got := replay(t, cs, 0)
			model, _ := cs.RequestBody["model"].(string)
			res, err := testClient().Chat(context.Background(),
				&gateway.ChatRequest{Messages: []gateway.Message{user("用一句话介绍你自己。")}},
				dsUp(srv), gateway.ModelConfig{UpstreamModel: model, Thinking: thinkingOf(cs.RequestBody)})
			if err != nil {
				t.Fatal(err)
			}
			checkRequest(t, cs.RequestBody, *got)
			if res.FinishReason != "stop" || len(res.Message.Content) < 3 || res.Usage.Estimated || res.Usage.TotalTokens == 0 {
				t.Fatalf("res %+v", res)
			}
			hasReasoning := res.Message.ReasoningContent != nil && *res.Message.ReasoningContent != ""
			if hasReasoning != (thinkingOf(cs.RequestBody) == "on") {
				t.Fatalf("reasoning_content 透传不符：%v", hasReasoning)
			}
			if thinkingOf(cs.RequestBody) == "on" && res.Usage.ReasoningTokens == 0 {
				t.Fatal("思考模式应解析 reasoning_tokens")
			}
		})
	}
}

func TestDeepSeekContractStream(t *testing.T) {
	all := loadCassettes(t)
	for _, name := range []string{"stream-thinking", "stream-no-thinking"} {
		t.Run(name, func(t *testing.T) {
			cs := all[name]
			srv, got := replay(t, cs, 0)
			model, _ := cs.RequestBody["model"].(string)
			var content, reasoning strings.Builder
			finish := ""
			res, err := testClient().Stream(context.Background(),
				&gateway.ChatRequest{Messages: []gateway.Message{user("用一句话介绍你自己。")}, Stream: true},
				dsUp(srv), gateway.ModelConfig{UpstreamModel: model, Thinking: thinkingOf(cs.RequestBody)},
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
			checkRequest(t, cs.RequestBody, *got)
			if finish != cs.FinishReason || res.FinishReason != cs.FinishReason || content.Len() == 0 {
				t.Fatalf("finish=%q content=%q", finish, content.String())
			}
			wantTotal := int(cs.Usage["total_tokens"].(float64))
			if res.Usage.Estimated || res.Usage.TotalTokens != wantTotal {
				t.Fatalf("usage 应取自最后分片：%+v，期望 total=%d", res.Usage, wantTotal)
			}
			if (reasoning.Len() > 0) != (thinkingOf(cs.RequestBody) == "on") {
				t.Fatalf("流式 reasoning_content 透传不符：%q", reasoning.String())
			}
		})
	}
}

func TestDeepSeekContractErrors(t *testing.T) {
	all := loadCassettes(t)
	cases := []struct {
		name, code string
		status     int
	}{
		{"err-invalid-key", gateway.CodeUpstreamAuth, 502},
		{"err-invalid-model", gateway.CodeUpstreamBadRequest, 400},
		{"err-context-too-long", gateway.CodeUpstreamBadRequest, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := replay(t, all[tc.name], 0)
			_, err := testClient().Chat(context.Background(),
				&gateway.ChatRequest{Messages: []gateway.Message{user("hi")}}, dsUp(srv),
				gateway.ModelConfig{UpstreamModel: "deepseek-flash", Thinking: "off"})
			if err == nil || err.Code != tc.code || err.Status != tc.status {
				t.Fatalf("err %+v", err)
			}
			if strings.Contains(err.Message, testKey) {
				t.Fatalf("错误消息未脱敏：%s", err.Message)
			}
		})
	}
}

func TestDeepSeekContractTimeout(t *testing.T) {
	all := loadCassettes(t)
	for _, name := range []string{"chat-no-thinking", "stream-no-thinking"} {
		t.Run(name, func(t *testing.T) {
			cs := all[name]
			srv, _ := replay(t, cs, 3*time.Second)
			u := dsUp(srv)
			u.Timeout = 200 * time.Millisecond
			req := &gateway.ChatRequest{Messages: []gateway.Message{user("hi")}}
			cfg := gateway.ModelConfig{UpstreamModel: "deepseek-flash", Thinking: "off"}
			var err *gateway.Error
			if cs.Chunks == nil {
				_, err = testClient().Chat(context.Background(), req, u, cfg)
			} else {
				_, err = testClient().Stream(context.Background(), req, u, cfg, func(gateway.Delta) error { return nil })
			}
			if err == nil || err.Code != gateway.CodeUpstreamTimeout {
				t.Fatalf("timeout: %v", err)
			}
		})
	}
}
