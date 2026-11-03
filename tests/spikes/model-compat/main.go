// OpenAI 兼容性探针：对任意 /v1/chat/completions 端点跑 4 个用例并输出原始观察。
// 只用标准库。用法：go run . -base http://127.0.0.1:11434/v1 -model qwen2.5:1.5b [-key $KEY] -out ollama.json
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

var (
	base  = flag.String("base", "http://127.0.0.1:11434/v1", "base url")
	model = flag.String("model", "qwen2.5:1.5b", "model")
	key   = flag.String("key", "", "api key (或环境变量 OPENAI_API_KEY)")
	out   = flag.String("out", "", "json output")
	toolQ = flag.String("toolq", "帮我查一下工单 T-1 的状态", "tool 用例的用户问题")
)

var tools = []any{map[string]any{"type": "function", "function": map[string]any{
	"name": "query_ticket", "description": "按工单号查询工单状态",
	"parameters": map[string]any{"type": "object", "properties": map[string]any{
		"id": map[string]any{"type": "string", "description": "工单号，如 T-1"}}, "required": []string{"id"}}}}}

type Case struct {
	Name     string         `json:"name"`
	OK       bool           `json:"ok"`
	Status   int            `json:"http_status"`
	Err      string         `json:"error,omitempty"`
	Obs      map[string]any `json:"observations"`
	Millis   int64          `json:"elapsed_ms"`
	TTFTMs   int64          `json:"ttft_ms,omitempty"`
	RawFirst string         `json:"raw_first_chunk,omitempty"`
	RawLast  string         `json:"raw_last_chunks,omitempty"`
}

func post(body map[string]any) (*http.Response, error) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", strings.TrimRight(*base, "/")+"/chat/completions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	k := *key
	if k == "" {
		k = os.Getenv("OPENAI_API_KEY")
	}
	if k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	return (&http.Client{Timeout: 180 * time.Second}).Do(req)
}

func msgs(tool bool) []any {
	if tool {
		return []any{map[string]any{"role": "user", "content": *toolQ}}
	}
	return []any{map[string]any{"role": "user", "content": "用一句话介绍杭州。"}}
}

func nonStream(name string, withTools bool, extra ...map[string]any) Case {
	c := Case{Name: name, Obs: map[string]any{}}
	t := time.Now()
	body := map[string]any{"model": *model, "messages": msgs(withTools), "temperature": 0}
	if withTools {
		body["tools"] = tools
	}
	for _, e := range extra {
		for k, v := range e {
			body[k] = v
		}
	}
	resp, err := post(body)
	if err != nil {
		c.Err = err.Error()
		return c
	}
	defer resp.Body.Close()
	c.Status = resp.StatusCode
	raw, _ := io.ReadAll(resp.Body)
	c.Millis = time.Since(t).Milliseconds()
	var r map[string]any
	if err := json.Unmarshal(raw, &r); err != nil {
		c.Err = "bad json: " + string(raw)
		return c
	}
	c.RawFirst = string(raw)
	c.Obs["usage"] = r["usage"]
	if ch, ok := r["choices"].([]any); ok && len(ch) > 0 {
		c0 := ch[0].(map[string]any)
		c.Obs["finish_reason"] = c0["finish_reason"]
		m := c0["message"].(map[string]any)
		c.Obs["content"] = m["content"]
		if tc, ok := m["tool_calls"]; ok {
			c.Obs["tool_calls"] = tc
			if arr, ok := tc.([]any); ok && len(arr) > 0 {
				f := arr[0].(map[string]any)["function"].(map[string]any)
				_, isStr := f["arguments"].(string)
				c.Obs["arguments_is_json_string"] = isStr
			}
		}
	}
	c.OK = resp.StatusCode == 200
	return c
}

func stream(name string, withTools, includeUsage bool) Case {
	c := Case{Name: name, Obs: map[string]any{}}
	t := time.Now()
	body := map[string]any{"model": *model, "messages": msgs(withTools), "temperature": 0, "stream": true}
	if includeUsage {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if withTools {
		body["tools"] = tools
	}
	resp, err := post(body)
	if err != nil {
		c.Err = err.Error()
		return c
	}
	defer resp.Body.Close()
	c.Status = resp.StatusCode
	c.Obs["content_type"] = resp.Header.Get("Content-Type")
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var chunks, toolChunks int
	var content, args strings.Builder
	var last []string
	var finish any
	var usage any
	var usageInNonFinal bool
	var sawDone bool
	toolIDs := map[string]bool{}
	for sc.Scan() {
		l := sc.Text()
		if !strings.HasPrefix(l, "data:") {
			continue
		}
		d := strings.TrimSpace(strings.TrimPrefix(l, "data:"))
		if d == "[DONE]" {
			sawDone = true
			continue
		}
		chunks++
		if chunks == 1 {
			c.TTFTMs = time.Since(t).Milliseconds()
			c.RawFirst = d
		}
		last = append(last, d)
		if len(last) > 3 {
			last = last[1:]
		}
		var r map[string]any
		if json.Unmarshal([]byte(d), &r) != nil {
			continue
		}
		if u, ok := r["usage"]; ok && u != nil {
			usage = u
			if ch, _ := r["choices"].([]any); len(ch) > 0 {
				usageInNonFinal = true
			}
		}
		ch, _ := r["choices"].([]any)
		if len(ch) == 0 {
			continue
		}
		c0 := ch[0].(map[string]any)
		if fr := c0["finish_reason"]; fr != nil {
			finish = fr
		}
		dl, _ := c0["delta"].(map[string]any)
		if s, ok := dl["content"].(string); ok {
			content.WriteString(s)
		}
		if tc, ok := dl["tool_calls"].([]any); ok {
			toolChunks++
			for _, x := range tc {
				m := x.(map[string]any)
				if id, ok := m["id"].(string); ok && id != "" {
					toolIDs[id] = true
				}
				if _, ok := m["index"]; !ok {
					c.Obs["tool_call_missing_index"] = true
				}
				if f, ok := m["function"].(map[string]any); ok {
					if a, ok := f["arguments"].(string); ok {
						args.WriteString(a)
					}
				}
			}
		}
	}
	c.Millis = time.Since(t).Milliseconds()
	c.RawLast = strings.Join(last, "\n")
	c.Obs["chunks"] = chunks
	c.Obs["saw_DONE"] = sawDone
	c.Obs["finish_reason"] = finish
	c.Obs["usage"] = usage
	c.Obs["usage_on_chunk_with_choices"] = usageInNonFinal
	c.Obs["content"] = content.String()
	if withTools {
		c.Obs["tool_call_delta_chunks"] = toolChunks
		c.Obs["tool_call_ids"] = len(toolIDs)
		c.Obs["arguments_concat"] = args.String()
		c.Obs["arguments_valid_json"] = json.Valid([]byte(args.String()))
	}
	c.OK = resp.StatusCode == 200 && chunks > 0
	return c
}

func main() {
	flag.Parse()
	cases := []Case{
		nonStream("chat", false),
		stream("chat_stream_no_stream_options", false, false),
		stream("chat_stream_include_usage", false, true),
		nonStream("tools", true),
		nonStream("tools_choice_required", true, map[string]any{"tool_choice": "required"}),
		stream("tools_stream_include_usage", true, true),
	}
	b, _ := json.MarshalIndent(map[string]any{"base": *base, "model": *model, "at": time.Now().Format(time.RFC3339), "cases": cases}, "", "  ")
	if *out != "" {
		os.WriteFile(*out, b, 0o644)
	}
	fmt.Println(string(b))
}
