// cassette-record 在本机对真实上游录制契约测试回放带（D-35），用于替换「文档构造」回放带。
//
// 只保存请求体与响应体（不保存任何请求头）；密钥只从 HENGDESK_RECORD_API_KEY 读取，
// 写盘前会把密钥替换为 [REDACTED] 并再次断言文件中不含密钥。不要在 CI 中运行。
//
//	HENGDESK_RECORD_API_KEY=... go run ./cmd/cassette-record -vendor dashscope \
//	  -base https://dashscope.aliyuncs.com/compatible-mode/v1 -model qwen-plus \
//	  -out internal/gateway/testdata/dashscope_cassette.json
//
// 出站请求经统一出站客户端（白名单 + DNS rebinding 防护），并把出站日志写到 -egress-log（JSON Lines，
// 默认 cassette-record-egress.jsonl，只含方法 / 主机 / 端口 / 路径 / 结果，不含请求头与密钥）。D-39 第 3 条：
// 真正发请求的命令行工具必须留出站日志；日志文件打不开或写不进去时不发请求。
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/egress"
)

const keyEnv = "HENGDESK_RECORD_API_KEY"

type event struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type rec struct {
	SourceCase   string            `json:"source_case"`
	Status       int               `json:"status"`
	RequestBody  map[string]any    `json:"request_body"`
	Body         json.RawMessage   `json:"body,omitempty"`
	Chunks       []json.RawMessage `json:"chunks,omitempty"`
	Events       []event           `json:"events,omitempty"`
	DoneMarker   bool              `json:"done_marker,omitempty"`
	FinishReason string            `json:"finish_reason,omitempty"`
	Usage        map[string]any    `json:"usage,omitempty"`
}

func main() {
	vendor := flag.String("vendor", "", "dashscope | vllm | anthropic")
	base := flag.String("base", "", "上游 base URL")
	model := flag.String("model", "", "上游模型名")
	out := flag.String("out", "", "输出文件")
	egressLog := flag.String("egress-log", "cassette-record-egress.jsonl", "出站日志文件（JSON Lines，追加写入）")
	allow := flag.String("allow", "", "额外出站白名单（如 vLLM 内网：\"cidr 10.0.0.0/8 8000\"）；默认只放行 -base 的主机")
	flag.Parse()
	key := os.Getenv(keyEnv)
	if *vendor == "" || *base == "" || *model == "" || *out == "" {
		fail("用法见文件头注释")
	}
	if key == "" && *vendor != "vllm" {
		fail("请设置 " + keyEnv + "（不要使用其他环境变量中的密钥）")
	}
	anth := *vendor == "anthropic"
	u, err := url.Parse(*base)
	if err != nil || u.Hostname() == "" {
		fail("-base 无效")
	}
	specs := []string{"host " + u.Hostname()}
	if _, e := netip.ParseAddr(u.Hostname()); e == nil {
		specs = []string{"ip " + u.Hostname()}
	}
	if *allow != "" {
		specs = append(specs, *allow)
	}
	// 与网关一致：经统一出站客户端（白名单 + DNS rebinding 防护 + 不走环境代理）。
	lf, err := os.OpenFile(*egressLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fail("无法打开出站日志文件：" + err.Error())
	}
	defer func() { _ = lf.Close() }()
	hc = (&egress.Client{Policy: egress.MustStatic(specs...), Sink: &egress.JSONLSink{W: lf}}).HTTP()
	msgs := []map[string]any{{"role": "user", "content": "用一句话介绍你自己。"}}
	think := func(on bool) map[string]any {
		switch *vendor {
		case "dashscope":
			return map[string]any{"enable_thinking": on}
		case "vllm":
			return map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": on}}
		default:
			if on {
				return map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 2048}}
			}
			return nil
		}
	}
	body := func(stream, on bool, m string, extra map[string]any) map[string]any {
		b := map[string]any{"model": m, "messages": msgs}
		if anth {
			b["max_tokens"] = 4096
		}
		if stream {
			b["stream"] = true
			if !anth {
				b["stream_options"] = map[string]any{"include_usage": true}
			}
		}
		for k, v := range think(on) {
			b[k] = v
		}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	long := strings.Repeat("很长的输入。", 200000)
	type c struct {
		name   string
		b      map[string]any
		badKey bool
	}
	cases := []c{
		{"chat-thinking", body(false, true, *model, nil), false},
		{"chat-no-thinking", body(false, false, *model, nil), false},
		{"stream-thinking", body(true, true, *model, nil), false},
		{"stream-no-thinking", body(true, false, *model, nil), false},
		{"err-invalid-key", body(false, false, *model, nil), true},
		{"err-invalid-model", body(false, false, "hengdesk-no-such-model", nil), false},
		{"err-context-too-long", body(false, false, *model, map[string]any{"messages": []map[string]any{{"role": "user", "content": long}}}), false},
	}
	all := map[string]rec{}
	for _, cs := range cases {
		k := key
		if cs.badKey {
			k = "sk-invalid-for-recording"
		}
		r, err := call(*base, anth, k, cs.b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v（跳过）\n", cs.name, err)
			continue
		}
		r.SourceCase = *vendor + "-recorded#" + cs.name
		if cs.name == "err-context-too-long" {
			r.RequestBody = map[string]any{"model": *model, "note": "超长输入已省略"}
		}
		all[cs.name] = r
		fmt.Fprintf(os.Stderr, "%s: status %d\n", cs.name, r.Status)
	}
	f := map[string]any{"_note": "真实录制 " + time.Now().Format("2006-01-02") + "（cmd/cassette-record，已脱敏）", "cases": all}
	b, _ := json.MarshalIndent(f, "", "  ")
	for _, s := range []string{key, "sk-invalid-for-recording"} {
		if s != "" {
			b = bytes.ReplaceAll(b, []byte(s), []byte("[REDACTED]"))
		}
	}
	if key != "" && bytes.Contains(b, []byte(key)) {
		fail("脱敏失败，拒绝写盘")
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		fail(err.Error())
	}
	fmt.Fprintln(os.Stderr, "已写入", *out)
}

var hc *http.Client

func call(base string, anth bool, key string, body map[string]any) (rec, error) {
	target := strings.TrimRight(base, "/")
	if anth {
		if !strings.HasSuffix(target, "/v1") {
			target += "/v1"
		}
		target += "/messages"
	} else {
		target += "/chat/completions"
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, target, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if anth {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	ctx, cancel := context.WithTimeout(egress.WithCaller(context.Background(), egress.Caller{Module: "cli.cassette-record"}), 120*time.Second)
	defer cancel()
	resp, err := hc.Do(req.WithContext(ctx))
	if err != nil {
		return rec{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	r := rec{Status: resp.StatusCode, RequestBody: body}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if json.Valid(raw) {
			r.Body = raw
		} else {
			r.Body, _ = json.Marshal(string(raw))
		}
		return r, nil
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	name := ""
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "event:"); ok {
			name = strings.TrimSpace(v)
			continue
		}
		v, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "[DONE]" {
			r.DoneMarker = true
			continue
		}
		if anth {
			r.Events = append(r.Events, event{name, json.RawMessage(v)})
		} else {
			r.Chunks = append(r.Chunks, json.RawMessage(v))
		}
	}
	if r.Status == 200 {
		r.FinishReason = "stop"
		r.Usage = map[string]any{"total_tokens": totalTokens(r)}
	}
	return r, sc.Err()
}

// totalTokens 从录制结果推算网关应得到的 total_tokens，供契约测试比对。
func totalTokens(r rec) int {
	var total int
	for _, ch := range r.Chunks {
		var c struct {
			Usage *struct {
				Total int `json:"total_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(ch, &c) == nil && c.Usage != nil {
			total = c.Usage.Total
		}
	}
	in, out := 0, 0
	for _, e := range r.Events {
		var d struct {
			Message struct {
				Usage map[string]int `json:"usage"`
			} `json:"message"`
			Usage map[string]int `json:"usage"`
		}
		if json.Unmarshal(e.Data, &d) != nil {
			continue
		}
		if u := d.Message.Usage; u != nil {
			in = u["input_tokens"] + u["cache_read_input_tokens"] + u["cache_creation_input_tokens"]
			out = u["output_tokens"]
		}
		if v, ok := d.Usage["output_tokens"]; ok {
			out = v
		}
	}
	if len(r.Events) > 0 {
		total = in + out
	}
	return total
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "cassette-record:", msg)
	os.Exit(1)
}
