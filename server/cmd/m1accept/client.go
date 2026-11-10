package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const (
	csrfHeader  = "X-CSRF-Token"
	csrfCookie  = "hd_csrf"
	traceHeader = "X-Trace-Id"
	aigcHeader  = "X-HengDesk-AIGC"
)

// Client 带 cookie 会话与 CSRF 的 HTTP 客户端。
type Client struct {
	Base  *url.URL
	HTTP  *http.Client
	CSRF  string
	Email string
}

func newClient(base string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Client{Base: u, HTTP: &http.Client{Jar: jar, Timeout: 2 * time.Minute}}, nil
}

func (c *Client) url(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.Base.String() + path
}

type httpError struct {
	Status int
	Body   string
}

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, trimBody(e.Body)) }

func trimBody(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 400 {
		return string([]rune(s)[:400]) + "…"
	}
	return s
}

func (c *Client) do(method, path string, body any, want int) ([]byte, http.Header, error) {
	var rdr io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rdr = bytes.NewReader(b)
		ct = "application/json"
	case string:
		rdr = strings.NewReader(b)
		ct = "application/json"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return nil, nil, err
		}
		rdr = bytes.NewReader(raw)
		ct = "application/json"
	}
	req, err := http.NewRequest(method, c.url(path), rdr)
	if err != nil {
		return nil, nil, err
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if c.CSRF != "" && method != http.MethodGet && method != http.MethodHead {
		req.Header.Set(csrfHeader, c.CSRF)
	}
	req.Header.Set("Accept", "application/json, text/event-stream, */*")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, res.Header, err
	}
	c.captureCSRF(res)
	if want > 0 && res.StatusCode != want {
		return b, res.Header, &httpError{Status: res.StatusCode, Body: string(b)}
	}
	return b, res.Header, nil
}

func (c *Client) captureCSRF(res *http.Response) {
	for _, ck := range res.Cookies() {
		if ck.Name == csrfCookie && ck.Value != "" {
			c.CSRF = ck.Value
		}
	}
}

func (c *Client) login(email, password string) error {
	b, _, err := c.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password}, 200)
	if err != nil {
		return fmt.Errorf("登录 %s 失败：%w", email, err)
	}
	var out struct {
		CSRFToken string `json:"csrf_token"`
		Email     string `json:"email"`
	}
	_ = json.Unmarshal(b, &out)
	if out.CSRFToken != "" {
		c.CSRF = out.CSRFToken
	}
	c.Email = email
	return nil
}

func (c *Client) json(method, path string, body any, want int, dest any) (http.Header, error) {
	b, h, err := c.do(method, path, body, want)
	if err != nil {
		return h, err
	}
	if dest != nil && len(bytes.TrimSpace(b)) > 0 && want != http.StatusNoContent {
		if err := json.Unmarshal(b, dest); err != nil {
			return h, fmt.Errorf("解析 JSON 失败：%w；正文 %s", err, trimBody(string(b)))
		}
	}
	return h, nil
}

func extractID(raw []byte) int {
	var top map[string]any
	if json.Unmarshal(raw, &top) != nil {
		return 0
	}
	if id := asInt(top["id"]); id != 0 {
		return id
	}
	for _, k := range []string{"file", "item", "data", "conversation", "model", "provider", "budget", "kb", "agent"} {
		if nested, ok := top[k].(map[string]any); ok {
			if id := asInt(nested["id"]); id != 0 {
				return id
			}
		}
	}
	return 0
}

func (c *Client) getID(method, path string, body any, want int) (int, error) {
	b, _, err := c.do(method, path, body, want)
	if err != nil {
		return 0, err
	}
	id := extractID(b)
	if id == 0 {
		return 0, fmt.Errorf("%s %s 响应没有 id：%s", method, path, trimBody(string(b)))
	}
	return id, nil
}

func (c *Client) upload(path, filename string, content []byte, want int) ([]byte, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(content); err != nil {
		return nil, err
	}
	_ = w.Close()
	req, err := http.NewRequest(http.MethodPost, c.url(path), &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if c.CSRF != "" {
		req.Header.Set(csrfHeader, c.CSRF)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	c.captureCSRF(res)
	if want > 0 && res.StatusCode != want {
		return b, &httpError{Status: res.StatusCode, Body: string(b)}
	}
	return b, nil
}

// SSEEvent 一条 SSE 事件。
type SSEEvent struct {
	Event string
	Data  string
}

func (c *Client) sse(path string, body any) ([]SSEEvent, http.Header, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.url(path), bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.CSRF != "" {
		req.Header.Set(csrfHeader, c.CSRF)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, res.Header, err
	}
	c.captureCSRF(res)
	if res.StatusCode != 200 {
		return nil, res.Header, &httpError{Status: res.StatusCode, Body: string(b)}
	}
	return parseSSE(string(b)), res.Header, nil
}

func parseSSE(s string) []SSEEvent {
	var evs []SSEEvent
	var ev, data string
	flush := func() {
		if ev == "" && data == "" {
			return
		}
		evs = append(evs, SSEEvent{Event: ev, Data: strings.TrimSuffix(data, "\n")})
		ev, data = "", ""
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "event:"):
			ev = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			if data != "" {
				data += "\n"
			}
			data += chunk
		}
	}
	flush()
	return evs
}

func jsonGet(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case string:
		var i int
		_, _ = fmt.Sscanf(n, "%d", &i)
		return i
	default:
		return 0
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
