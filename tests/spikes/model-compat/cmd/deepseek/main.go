// DeepSeek OpenAI 兼容接口实测。密钥只通过 os.Getenv("DEEPSEEK_API_KEY") 读取，
// 不打印、不落盘；日志中 Authorization 一律写为脱敏占位符。
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const base = "https://api.deepseek.com"

var (
	apiKey  string
	calls   int
	maxCall = flag.Int("max", 100, "最大调用次数")
	outDir  = flag.String("out", "deepseek-runs", "输出目录")
	only    = flag.String("only", "", "只跑名字包含该子串的用例")
	records []Rec
	client  = &http.Client{Timeout: 180 * time.Second}
)

type ToolDelta struct {
	Chunk    int    `json:"chunk"`
	Index    int    `json:"index"`
	HasID    bool   `json:"has_id"`
	HasName  bool   `json:"has_name"`
	ArgsFrag string `json:"args_frag"`
}

type Rec struct {
	Case        string            `json:"case"`
	Model       string            `json:"model"`
	Thinking    string            `json:"thinking,omitempty"`
	Stream      bool              `json:"stream"`
	Status      int               `json:"status"`
	ReqHeaders  map[string]string `json:"request_headers"`
	ReqBody     any               `json:"request_body"`
	RespHeaders map[string]string `json:"response_headers"`
	TTFTms      float64           `json:"ttft_ms,omitempty"`        // 首个任意 delta（reasoning/content/tool）
	TTFCms      float64           `json:"ttft_content_ms,omitempty"` // 首个 content delta
	TotalMs     float64           `json:"total_ms"`
	Body        any               `json:"body,omitempty"` // 非流式响应 / 错误体
	// 流式分析
	Chunks          int         `json:"chunks,omitempty"`
	Done            bool        `json:"done_marker,omitempty"`
	UsageChunk      int         `json:"usage_chunk_index,omitempty"` // 1-based；0 = 无
	UsageChunkChoic int         `json:"usage_chunk_choices_len,omitempty"`
	UsageWithFinish bool        `json:"usage_chunk_has_finish_reason,omitempty"`
	FinishChunk     int         `json:"finish_chunk_index,omitempty"`
	FinishReason    string      `json:"finish_reason,omitempty"`
	Usage           any         `json:"usage,omitempty"`
	ReasonChunks    int         `json:"reasoning_chunks,omitempty"`
	ContentChunks   int         `json:"content_chunks,omitempty"`
	Content         string      `json:"content,omitempty"`
	Reasoning       string      `json:"reasoning_excerpt,omitempty"`
	ToolDeltas      []ToolDelta `json:"tool_deltas,omitempty"`
	FirstChunk      any         `json:"first_chunk,omitempty"`
	LastChunks      []any       `json:"last_chunks,omitempty"`
	DeltaKeys       []string    `json:"delta_keys_seen,omitempty"`
	Note            string      `json:"note,omitempty"`
	Err             string      `json:"error,omitempty"`
}

func redactHeaders(h http.Header) map[string]string {
	m := map[string]string{}
	for k, v := range h {
		val := strings.Join(v, ", ")
		if apiKey != "" && strings.Contains(val, apiKey) {
			val = "<REDACTED>"
		}
		if strings.EqualFold(k, "Set-Cookie") {
			val = "<omitted>"
		}
		m[k] = val
	}
	return m
}

func trimBody(b map[string]any) any {
	// 超长输入只保留长度，避免日志膨胀
	raw, _ := json.Marshal(b)
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	if ms, ok := c["messages"].([]any); ok {
		for _, m := range ms {
			mm, _ := m.(map[string]any)
			if s, ok := mm["content"].(string); ok && len(s) > 2000 {
				mm["content"] = fmt.Sprintf("<%d bytes omitted>", len(s))
			}
		}
	}
	return c
}

func do(name, method, path string, body map[string]any, key string) (*http.Response, *Rec, error) {
	if calls >= *maxCall {
		return nil, nil, fmt.Errorf("call budget exhausted")
	}
	calls++
	r := &Rec{Case: name, ReqHeaders: map[string]string{"Authorization": "Bearer <REDACTED>", "Content-Type": "application/json"}}
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
		r.ReqBody = trimBody(body)
		if m, ok := body["model"].(string); ok {
			r.Model = m
		}
		if t, ok := body["thinking"].(map[string]any); ok {
			r.Thinking, _ = t["type"].(string)
		}
		if s, ok := body["stream"].(bool); ok {
			r.Stream = s
		}
	}
	req, _ := http.NewRequest(method, base+path, rd)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	return resp, r, err
}

func finish(r *Rec) { records = append(records, *r) }

func nonStream(name string, body map[string]any) map[string]any {
	return nonStreamKey(name, body, apiKey)
}

func nonStreamKey(name string, body map[string]any, key string) map[string]any {
	t0 := time.Now()
	resp, r, err := do(name, "POST", "/chat/completions", body, key)
	if r == nil {
		return nil
	}
	defer finish(r)
	if err != nil {
		r.Err = err.Error()
		return nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	r.TotalMs = ms(time.Since(t0))
	r.Status = resp.StatusCode
	r.RespHeaders = redactHeaders(resp.Header)
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		r.Body = string(raw)
		return nil
	}
	r.Body = m
	if u, ok := m["usage"]; ok {
		r.Usage = u
	}
	if ch, ok := m["choices"].([]any); ok && len(ch) > 0 {
		c0 := ch[0].(map[string]any)
		r.FinishReason, _ = c0["finish_reason"].(string)
	}
	return m
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// stream 返回聚合后的 assistant message（含 reasoning_content / tool_calls）
func stream(name string, body map[string]any) map[string]any {
	body["stream"] = true
	t0 := time.Now()
	resp, r, err := do(name, "POST", "/chat/completions", body, apiKey)
	if r == nil {
		return nil
	}
	defer finish(r)
	if err != nil {
		r.Err = err.Error()
		return nil
	}
	defer resp.Body.Close()
	r.Status = resp.StatusCode
	r.RespHeaders = redactHeaders(resp.Header)
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		var m any
		if json.Unmarshal(raw, &m) != nil {
			m = string(raw)
		}
		r.Body = m
		r.TotalMs = ms(time.Since(t0))
		return nil
	}
	keys := map[string]bool{}
	var content, reasoning strings.Builder
	type tc struct{ id, name, args string }
	tcs := map[int]*tc{}
	maxIdx := -1
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	var last []any
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			r.Done = true
			break
		}
		var ch map[string]any
		if json.Unmarshal([]byte(data), &ch) != nil {
			continue
		}
		r.Chunks++
		if r.Chunks == 1 {
			r.FirstChunk = ch
		}
		last = append(last, ch)
		if len(last) > 3 {
			last = last[1:]
		}
		choices, _ := ch["choices"].([]any)
		if u, ok := ch["usage"]; ok && u != nil {
			r.UsageChunk = r.Chunks
			r.UsageChunkChoic = len(choices)
			r.Usage = u
		}
		for _, c := range choices {
			cm := c.(map[string]any)
			if fr, ok := cm["finish_reason"].(string); ok && fr != "" {
				r.FinishReason = fr
				r.FinishChunk = r.Chunks
				if r.UsageChunk == r.Chunks {
					r.UsageWithFinish = true
				}
			}
			d, _ := cm["delta"].(map[string]any)
			for k, v := range d {
				if v != nil {
					keys[k] = true
				}
			}
			got := false
			if s, ok := d["reasoning_content"].(string); ok && s != "" {
				reasoning.WriteString(s)
				r.ReasonChunks++
				got = true
			}
			if s, ok := d["content"].(string); ok && s != "" {
				content.WriteString(s)
				r.ContentChunks++
				if r.TTFCms == 0 {
					r.TTFCms = ms(time.Since(t0))
				}
				got = true
			}
			if arr, ok := d["tool_calls"].([]any); ok {
				for _, a := range arr {
					am := a.(map[string]any)
					idx := int(am["index"].(float64))
					if idx > maxIdx {
						maxIdx = idx
					}
					t := tcs[idx]
					if t == nil {
						t = &tc{}
						tcs[idx] = t
					}
					td := ToolDelta{Chunk: r.Chunks, Index: idx}
					if id, ok := am["id"].(string); ok && id != "" {
						t.id = id
						td.HasID = true
					}
					if f, ok := am["function"].(map[string]any); ok {
						if n, ok := f["name"].(string); ok && n != "" {
							t.name = n
							td.HasName = true
						}
						if s, ok := f["arguments"].(string); ok {
							t.args += s
							td.ArgsFrag = s
						}
					}
					r.ToolDeltas = append(r.ToolDeltas, td)
					got = true
				}
			}
			if got && r.TTFTms == 0 {
				r.TTFTms = ms(time.Since(t0))
			}
		}
	}
	r.LastChunks = last
	r.TotalMs = ms(time.Since(t0))
	for k := range keys {
		r.DeltaKeys = append(r.DeltaKeys, k)
	}
	r.Content = content.String()
	rs := reasoning.String()
	if len([]rune(rs)) > 200 {
		rs = string([]rune(rs)[:200]) + "…"
	}
	r.Reasoning = rs
	msg := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		msg["reasoning_content"] = reasoning.String()
	}
	if maxIdx >= 0 {
		var arr []any
		for i := 0; i <= maxIdx; i++ {
			t := tcs[i]
			if t == nil {
				continue
			}
			ok := json.Valid([]byte(t.args))
			if !ok {
				r.Note += fmt.Sprintf("tool[%d] args 非法 JSON; ", i)
			}
			arr = append(arr, map[string]any{"id": t.id, "type": "function", "function": map[string]any{"name": t.name, "arguments": t.args}})
		}
		msg["tool_calls"] = arr
	}
	return msg
}

// ---------- 用例 ----------

var tools = []any{
	map[string]any{"type": "function", "function": map[string]any{
		"name": "get_weather", "description": "查询某城市某日期的天气",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"location": map[string]any{"type": "string", "description": "城市名"},
			"date":     map[string]any{"type": "string", "description": "日期 YYYY-MM-DD"}},
			"required": []string{"location", "date"}}}},
	map[string]any{"type": "function", "function": map[string]any{
		"name": "query_ticket", "description": "按工单号查询工单状态",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"id": map[string]any{"type": "string", "description": "工单号，如 T-1"}},
			"required": []string{"id"}}}},
}

func user(s string) []any { return []any{map[string]any{"role": "user", "content": s}} }

const flash = "deepseek-flash"

func req(model, mode string, m []any, extra map[string]any) map[string]any {
	b := map[string]any{"model": model, "messages": m}
	if mode != "" {
		b["thinking"] = map[string]any{"type": mode}
	}
	if mode == "enabled" {
		b["reasoning_effort"] = "low"
		b["max_tokens"] = 2048
	} else {
		b["max_tokens"] = 256
	}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func mockTool(name, args string) string {
	var a map[string]any
	_ = json.Unmarshal([]byte(args), &a)
	switch name {
	case "get_weather":
		return fmt.Sprintf(`{"location":%q,"date":%q,"weather":"多云","temp":"7~13°C"}`, a["location"], a["date"])
	case "query_ticket":
		return fmt.Sprintf(`{"id":%q,"title":"打印机故障","status":"处理中","assignee":"张三"}`, a["id"])
	}
	return `{"error":"unknown tool"}`
}

func toolCallsOf(msg map[string]any) []any {
	if msg == nil {
		return nil
	}
	if m, ok := msg["choices"].([]any); ok { // 非流式完整响应
		msg = m[0].(map[string]any)["message"].(map[string]any)
	}
	tc, _ := msg["tool_calls"].([]any)
	return tc
}

func assistantOf(resp map[string]any) map[string]any {
	if m, ok := resp["choices"].([]any); ok {
		return m[0].(map[string]any)["message"].(map[string]any)
	}
	return resp
}

// 多轮闭环：调用工具 → 回填结果 → 最终答案。dropReasoning=true 时故意不回传 reasoning_content。
func loop(name, mode string, streaming, dropReasoning bool) {
	msgs := user("帮我查一下工单 T-1 的状态，并告诉我北京 2026-11-03 的天气。")
	for round := 1; round <= 4; round++ {
		b := req(flash, mode, msgs, map[string]any{"tools": tools})
		cn := fmt.Sprintf("%s/round%d", name, round)
		var am map[string]any
		if streaming {
			b["stream_options"] = map[string]any{"include_usage": true}
			am = stream(cn, b)
		} else {
			resp := nonStream(cn, b)
			if resp == nil || resp["choices"] == nil {
				return
			}
			am = assistantOf(resp)
		}
		if am == nil {
			return
		}
		tcs := toolCallsOf(am)
		if len(tcs) == 0 {
			return
		}
		a2 := map[string]any{"role": "assistant", "content": am["content"], "tool_calls": tcs}
		if rc, ok := am["reasoning_content"]; ok && !dropReasoning {
			a2["reasoning_content"] = rc
		}
		msgs = append(msgs, a2)
		for _, t := range tcs {
			tm := t.(map[string]any)
			f := tm["function"].(map[string]any)
			msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": tm["id"], "content": mockTool(f["name"].(string), f["arguments"].(string))})
		}
	}
}

func run(name string, fn func()) {
	if *only != "" && !strings.Contains(name, *only) {
		return
	}
	fmt.Fprintf(os.Stderr, "[%d] %s\n", calls, name)
	fn()
}

func main() {
	flag.Parse()
	apiKey = os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "DEEPSEEK_API_KEY 未设置")
		os.Exit(2)
	}
	_ = os.MkdirAll(*outDir, 0o755)

	// 0. 认证 + 模型列表（失败即停）
	{
		t0 := time.Now()
		resp, r, err := do("models", "GET", "/models", nil, apiKey)
		if err != nil {
			fmt.Fprintln(os.Stderr, "models 请求失败:", err)
			os.Exit(3)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		r.Status, r.TotalMs, r.RespHeaders = resp.StatusCode, ms(time.Since(t0)), redactHeaders(resp.Header)
		var m any
		_ = json.Unmarshal(raw, &m)
		r.Body = m
		finish(r)
		if resp.StatusCode != 200 {
			save()
			fmt.Fprintf(os.Stderr, "认证/模型列表失败 status=%d，停止\n", resp.StatusCode)
			os.Exit(4)
		}
	}

	hello := "用一句话介绍你自己。"
	run("legacy-name/deepseek-chat", func() { nonStream("legacy-name/deepseek-chat", req("deepseek-chat", "", user(hello), nil)) })
	run("legacy-name/deepseek-reasoner", func() {
		nonStream("legacy-name/deepseek-reasoner", req("deepseek-reasoner", "", user(hello), map[string]any{"max_tokens": 2048}))
	})
	run("default-mode/flash-no-thinking-param", func() {
		nonStream("default-mode/flash-no-thinking-param", req(flash, "", user(hello), map[string]any{"max_tokens": 2048}))
	})

	for _, mode := range []string{"disabled", "enabled"} {
		p := "T-" + mode + "/"
		for i := 1; i <= 3; i++ {
			run(p+"chat", func() { nonStream(fmt.Sprintf("%schat#%d", p, i), req(flash, mode, user(hello), nil)) })
		}
		run(p+"stream-no-options", func() { stream(p+"stream-no-options", req(flash, mode, user(hello), nil)) })
		for i := 1; i <= 3; i++ {
			run(p+"stream-include-usage", func() {
				stream(fmt.Sprintf("%sstream-include-usage#%d", p, i), req(flash, mode, user(hello), map[string]any{"stream_options": map[string]any{"include_usage": true}}))
			})
		}
		single := user("帮我查一下工单 T-1 的状态")
		run(p+"tool-single", func() { nonStream(p+"tool-single", req(flash, mode, single, map[string]any{"tools": tools})) })
		for i := 1; i <= 2; i++ {
			run(p+"tool-single-stream", func() {
				stream(fmt.Sprintf("%stool-single-stream#%d", p, i), req(flash, mode, single, map[string]any{"tools": tools, "stream_options": map[string]any{"include_usage": true}}))
			})
		}
		par := user("请同时查询北京和上海 2026-11-03 的天气，并查询工单 T-1 的状态。")
		run(p+"tool-parallel", func() { nonStream(p+"tool-parallel", req(flash, mode, par, map[string]any{"tools": tools})) })
		run(p+"tool-parallel-stream", func() {
			stream(p+"tool-parallel-stream", req(flash, mode, par, map[string]any{"tools": tools, "stream_options": map[string]any{"include_usage": true}}))
		})
		chit := user("你好，今天心情不错。")
		run(p+"tool_choice-none", func() {
			nonStream(p+"tool_choice-none", req(flash, mode, single, map[string]any{"tools": tools, "tool_choice": "none"}))
		})
		run(p+"tool_choice-required", func() {
			nonStream(p+"tool_choice-required", req(flash, mode, chit, map[string]any{"tools": tools, "tool_choice": "required"}))
		})
		run(p+"tool_choice-named", func() {
			nonStream(p+"tool_choice-named", req(flash, mode, chit, map[string]any{"tools": tools,
				"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "query_ticket"}}}))
		})
		run(p+"loop", func() { loop(p+"loop", mode, false, false) })
		run(p+"loop-stream", func() { loop(p+"loop-stream", mode, true, false) })
		if mode == "enabled" {
			run(p+"loop-drop-reasoning", func() { loop(p+"loop-drop-reasoning", mode, false, true) })
		}
	}

	// v4-pro 抽样
	run("pro/stream-include-usage", func() {
		stream("pro/stream-include-usage", req("deepseek-v4-pro", "disabled", user(hello), map[string]any{"stream_options": map[string]any{"include_usage": true}}))
	})
	run("pro/thinking-tool-stream", func() {
		stream("pro/thinking-tool-stream", req("deepseek-v4-pro", "enabled", user("帮我查一下工单 T-1 的状态"), map[string]any{"tools": tools, "stream_options": map[string]any{"include_usage": true}}))
	})

	// 错误形态
	run("err/invalid-model", func() { nonStream("err/invalid-model", req("deepseek-not-exist", "disabled", user(hello), nil)) })
	run("err/invalid-key", func() { nonStreamKey("err/invalid-key", req(flash, "disabled", user(hello), nil), "sk-invalid-test-key") })
	run("err/max-tokens-too-large", func() {
		nonStream("err/max-tokens-too-large", req(flash, "disabled", user(hello), map[string]any{"max_tokens": 10_000_000}))
	})
	run("err/bad-tool-schema", func() {
		nonStream("err/bad-tool-schema", req(flash, "disabled", user(hello), map[string]any{"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "bad name!"}}}}))
	})
	run("err/context-too-long", func() {
		// 约 1.3M token（超过 1M 上下文）；预期在推理前被拒绝
		long := strings.Repeat("hello ", 1_300_000)
		nonStream("err/context-too-long", req(flash, "disabled", user(long), map[string]any{"max_tokens": 16}))
	})
	save()
	fmt.Fprintf(os.Stderr, "完成，共 %d 次 HTTP 调用\n", calls)
}

func save() {
	raw, _ := json.MarshalIndent(map[string]any{"calls": calls, "records": records}, "", "  ")
	s := string(raw)
	if apiKey != "" && strings.Contains(s, apiKey) { // 兜底：绝不落盘密钥
		s = strings.ReplaceAll(s, apiKey, "<REDACTED>")
	}
	_ = os.WriteFile(*outDir+"/deepseek-results.json", []byte(s), 0o644)
}
