package gateway_test

// 通义（DashScope OpenAI 兼容模式）、vLLM、Anthropic 契约测试（D-35 / D-49）。
//
// dashscope_cassette.json 只收录通义官方接口文档示例（来源 URL、获取时间、非语义变换写在每条 provenance）。
// 这些示例不是真实调用，也不是 vLLM 实测。vllm / anthropic 回放带仍是历史构造样本，
// 不作为通义证据；vLLM 真实协议冒烟另见 tests/acceptance/vllm-cpu-smoke.sh。

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
	{"dashscope", "openai", "dashscope", "/chat/completions", "qwen3.8-max", func(rb map[string]any) string {
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

func caseThinking(v vendorSpec, cs vendorCase) string {
	if cs.Exercise.Thinking != "" {
		return cs.Exercise.Thinking
	}
	return v.thinking(cs.RequestBody)
}

func checkVendorRequest(t *testing.T, v vendorSpec, cs vendorCase, got map[string]any) {
	t.Helper()
	if cs.Exercise.Thinking != "" {
		if caseThinking(v, cs) != v.thinking(got) {
			t.Errorf("思考开关：用例 %s，网关发出 %v", cs.Exercise.Thinking, got)
		}
		if fmt.Sprint(got["model"]) != v.model {
			t.Errorf("model：期望 %s，网关发出 %v", v.model, got["model"])
		}
		if cs.Exercise.Stream {
			if got["stream"] != true {
				t.Error("流式请求应带 stream=true")
			}
		} else if got["stream"] == true {
			t.Error("非流式请求不应带 stream=true")
		}
	} else {
		want := cs.RequestBody
		for _, k := range []string{"model", "stream"} {
			if fmt.Sprint(want[k]) != fmt.Sprint(got[k]) {
				t.Errorf("请求字段 %s：录制 %v，网关发出 %v", k, want[k], got[k])
			}
		}
		if v.thinking(want) != v.thinking(got) {
			t.Errorf("思考开关：录制 %v，网关发出 %v", want, got)
		}
	}
	stream := cs.Exercise.Stream
	if cs.Exercise.Thinking == "" {
		stream = cs.RequestBody["stream"] == true
	}
	if v.kind == gateway.KindAnthropic {
		if got["max_tokens"] == nil {
			t.Error("Anthropic 请求必须带 max_tokens")
		}
		if th, _ := got["thinking"].(map[string]any); th != nil && th["budget_tokens"] == nil {
			t.Error("thinking 必须带 budget_tokens")
		}
	} else if stream {
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
				th := caseThinking(v, cs)
				res, err := testClient().Chat(context.Background(),
					&gateway.ChatRequest{Messages: []gateway.Message{user("用一句话介绍你自己。")}},
					v.up(srv), gateway.ModelConfig{UpstreamModel: v.model, Thinking: th})
				if err != nil {
					t.Fatal(err)
				}
				checkVendorRequest(t, v, cs, *got)
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
				th := caseThinking(v, cs)
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
				checkVendorRequest(t, v, cs, *got)
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
		// 通义只保留官方文档里能对上的错误示例。没有原文的错误体不编造，因此不测。
		if v.file == "dashscope" {
			cases = cases[:1]
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

func TestDashScopeCassetteProvenance(t *testing.T) {
	b, err := os.ReadFile("testdata/dashscope_cassette.json")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("我是一个 AI 助手")) || bytes.Contains(b, []byte("按公开文档构造")) || bytes.Contains(b, []byte("chatcmpl-doc-")) {
		t.Fatal("dashscope 回放带仍含编造样本")
	}
	var f struct {
		Claim string                `json:"_claim"`
		Note  string                `json:"_note"`
		Cases map[string]vendorCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.Claim != "official_documentation_example_not_a_live_call" {
		t.Fatalf("claim=%q", f.Claim)
	}
	if !strings.Contains(f.Note, "不是真实调用") || !strings.Contains(f.Note, "不是 vLLM") {
		t.Fatalf("note 必须标明不是真实调用、也不是 vLLM 实测：%s", f.Note)
	}
	needles := map[string]string{
		"chat-no-thinking": "我是来自阿里云的超大规模预训练模型，我叫千问。",
		"chat-thinking":    "chatcmpl-8f472937-8de2-9e74-9803-21b7a0b141c5",
		"stream-thinking":  "chatcmpl-7fd87e06-84bb-904e-8c17-169a22bc04ed",
		"err-invalid-key":  "Invalid API-key provided.",
	}
	allowed := map[string]string{
		"https://help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope": "",
		"https://help.aliyun.com/en/model-studio/qwen-api-via-openai-chat-completions":   "",
	}
	required := []string{"chat-thinking", "chat-no-thinking", "stream-thinking", "stream-no-thinking", "err-invalid-key"}
	if _, ok := f.Cases["err-invalid-model"]; ok {
		t.Fatal("没有官方 JSON 的错误体不得写入回放带")
	}
	for _, name := range required {
		cs, ok := f.Cases[name]
		if !ok {
			t.Fatalf("缺少 %s", name)
		}
		p := cs.Provenance
		if p.Kind != "official_doc_example" || p.Claim != "not_a_live_call" {
			t.Fatalf("%s 来源种类不对：%+v", name, p)
		}
		if _, ok := allowed[p.SourceURL]; !ok || p.RetrievedAt == "" || p.Section == "" || len(p.Transformations) == 0 {
			t.Fatalf("%s 来源不完整：%+v", name, p)
		}
		if !strings.Contains(string(b), needles[name]) && needles[name] != "" {
			t.Fatalf("%s 缺少官方示例原文片段 %q", name, needles[name])
		}
	}
	// 流式非思考示例的原文是逐字「我是」「来自」，并带官方 usage 38。
	ns := f.Cases["stream-no-thinking"]
	if ns.Usage["total_tokens"].(float64) != 38 || ns.FinishReason != "stop" || !ns.DoneMarker {
		t.Fatalf("stream-no-thinking 与官方示例不一致：usage=%v finish=%s", ns.Usage, ns.FinishReason)
	}
	if f.Cases["stream-thinking"].Usage["total_tokens"].(float64) != 171 {
		t.Fatal("stream-thinking usage 不是官方示例的 171")
	}
	// 英文文档用省略号截断正文，并写明中间流式 chunk 已省略。这两条不是完整实录。
	// 测试锁定省略号和页面上印出的 chunk 数，补写成全文或补造中间 chunk 都会失败。
	think := f.Cases["chat-thinking"]
	if think.Provenance.Completeness != "publisher_truncated_not_a_full_transcript" || !bytes.Contains(think.Body, []byte("…")) {
		t.Fatal("非流式思考示例必须保留官方省略号，不能补成我们写的全文")
	}
	st := f.Cases["stream-thinking"]
	if st.Provenance.Completeness != "publisher_truncated_not_a_full_transcript" || len(st.Chunks) != 5 {
		t.Fatalf("流式思考示例必须保持页面印出的 5 个 chunk，实际 %d", len(st.Chunks))
	}
	joined := strings.Join(st.Provenance.Transformations, "\n")
	if !strings.Contains(joined, "没有补造被省略的中间 chunk") {
		t.Fatal("流式思考示例必须写明没有补造被省略的中间 chunk")
	}
	raw, _ := json.Marshal(f.Cases["chat-no-thinking"].Body)
	if !bytes.Contains(raw, []byte(`"total_tokens":40`)) && !bytes.Contains(raw, []byte(`"total_tokens": 40`)) {
		t.Fatalf("非流式官方 usage 丢失：%s", raw)
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
