package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
)

// Policy 提供当前白名单。实现需自行缓存；读取失败时出站一律拒绝（fail-closed）。
type Policy interface {
	Rules(ctx context.Context) ([]Rule, error)
}

// StaticPolicy 固定白名单（测试与 CLI 用）。
type StaticPolicy []Rule

func (s StaticPolicy) Rules(context.Context) ([]Rule, error) { return s, nil }

// MustStatic 由 "kind value [ports]" 形式生成固定白名单，如 "cidr 127.0.0.0/8"、"host api.example.com 443"。
func MustStatic(specs ...string) StaticPolicy {
	var out StaticPolicy
	for _, s := range specs {
		f := strings.Fields(s)
		for len(f) < 3 {
			f = append(f, "")
		}
		r, err := ParseRule(f[0], f[1], f[2])
		if err != nil {
			panic(err)
		}
		out = append(out, r)
	}
	return out
}

// Resolver DNS 解析（测试注入以模拟 rebinding）。
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Caller 出站调用方。
type Caller struct {
	UserID int
	Email  string
	Module string // model.chat / model.test / model.provider.test / oidc / mcp / connector ...
	// AuditedFailClosed 调用方声明：本次出站之前已经按 fail-closed 写好了审计（目前只有模型网关的对话请求）。
	// 只有声明了的调用方，在放行日志写不进去时才允许继续出站；未声明（默认）一律拒绝（D-38 第 9 条）。
	AuditedFailClosed bool
}

type callerKey struct{}

// WithCaller 标记出站调用方，写入出站日志与审计。
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom 取调用方。
func CallerFrom(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}

// DeniedError 出站被拒绝。
type DeniedError struct {
	Host   string
	Port   int
	IP     string
	Reason string
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("egress denied: %s:%d (%s)", e.Host, e.Port, e.Reason)
}

// Message 给最终用户 / 管理员看的说明。
func (e *DeniedError) Message() string {
	return fmt.Sprintf("出站被拒绝：%s:%d，%s。请管理员在「出站控制」中添加白名单。", e.Host, e.Port, ReasonText(e.Reason))
}

// Record 一条出站日志。
type Record struct {
	// ID Sink.Start 返回的记录号；Write 时大于 0 表示更新这条记录。
	ID         int
	TS         time.Time
	Caller     Caller
	Method     string
	Scheme     string
	Host       string
	Port       int
	Path       string
	IP         string
	Allowed    bool
	Reason     string
	Status     int
	DurationMS int64
	BytesOut   int64
	BytesIn    int64
	Error      string
}

// Sink 出站日志落地。
//
// 放行的请求在发出之前先 Start 写一条记录（D-38 第 9 条：日志写不进去时，未声明审计 fail-closed 的调用方直接拒绝），
// 请求结束后 Write 用同一个 ID 回填结果；拒绝的请求直接 Write。
type Sink interface {
	Start(ctx context.Context, r Record) (int, error)
	Write(ctx context.Context, r Record) error
}

// Client 统一出站客户端。
type Client struct {
	Policy   Policy
	Sink     Sink         // 可为 nil（不落日志，仅测试）
	Audit    audit.Writer // 拒绝时写审计；nil 表示不可用，按 fail-closed 处理仍拒绝
	Log      *slog.Logger
	Resolver Resolver
	// MaxRedirects 默认 5。
	MaxRedirects int

	once sync.Once
	hc   *http.Client
	tr   *http.Transport
}

// HTTP 返回经过出站控制的 *http.Client（并发安全，复用连接池）。
func (c *Client) HTTP() *http.Client {
	c.once.Do(func() {
		c.tr = &http.Transport{
			Proxy:                 nil, // 不走环境代理：代理会绕过对目标 IP 的检查
			DialContext:           c.dial,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: time.Second,
		}
		c.hc = &http.Client{Transport: &logTransport{c: c, base: c.tr}, CheckRedirect: c.checkRedirect}
	})
	return c.hc
}

// CloseIdle 白名单变更后关闭空闲连接，使新规则对复用连接也立即生效。
func (c *Client) CloseIdle() {
	if c.tr != nil {
		c.tr.CloseIdleConnections()
	}
}

func (c *Client) logger() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c *Client) resolver() Resolver {
	if c.Resolver != nil {
		return c.Resolver
	}
	return net.DefaultResolver
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	max := c.MaxRedirects
	if max == 0 {
		max = 5
	}
	if len(via) >= max {
		return &DeniedError{Host: req.URL.Hostname(), Port: portOf(req.URL), Reason: ReasonRedirects}
	}
	// 新的一跳会重新经过 logTransport 与 dial 的检查；跨主机重定向不转发 Authorization（标准库行为）。
	return nil
}

// hop 一跳的检查结果，由 dial 回填给 logTransport。
type hop struct {
	mu     sync.Mutex
	ip     string
	denied *DeniedError
}

type hopKey struct{}

func (c *Client) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, ps, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, _ := strconv.Atoi(ps)
	h, _ := ctx.Value(hopKey{}).(*hop)
	deny := func(ip, reason string) error {
		d := &DeniedError{Host: host, Port: port, IP: ip, Reason: reason}
		if h != nil {
			h.mu.Lock()
			h.denied, h.ip = d, ip
			h.mu.Unlock()
		}
		return d
	}
	rules, err := c.Policy.Rules(ctx)
	if err != nil {
		c.logger().Error("egress policy unavailable, denying", "err", err)
		return nil, deny("", ReasonPolicyError)
	}
	if len(rules) == 0 {
		return nil, deny("", ReasonEmpty)
	}
	var addrs []netip.Addr
	if a, perr := netip.ParseAddr(host); perr == nil {
		addrs = []netip.Addr{a}
	} else {
		if ok, why := HostAllowed(rules, host, port); !ok {
			return nil, deny("", why)
		}
		addrs, err = c.resolver().LookupNetIP(ctx, "ip", host)
		if err != nil || len(addrs) == 0 {
			return nil, deny("", ReasonDNS)
		}
	}
	// 任一解析结果不允许即整体拒绝（防止混入内网地址的记录）。
	for _, a := range addrs {
		if ok, why := Decide(rules, host, port, a); !ok {
			return nil, deny(a.Unmap().String(), why)
		}
	}
	var d net.Dialer
	d.Timeout = 30 * time.Second
	var lastErr error
	for _, a := range addrs {
		// 直接连接已检查过的 IP，不再二次解析（防 DNS rebinding）。
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(a.Unmap().String(), ps))
		if err == nil {
			if h != nil {
				h.mu.Lock()
				h.ip = a.Unmap().String()
				h.mu.Unlock()
			}
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func portOf(u *url.URL) int {
	if p := u.Port(); p != "" {
		n, _ := strconv.Atoi(p)
		return n
	}
	if u.Scheme == "http" {
		return 80
	}
	return 443
}

type logTransport struct {
	c    *Client
	base http.RoundTripper
}

func (t *logTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	u := req.URL
	rec := Record{TS: start, Caller: CallerFrom(req.Context()), Method: req.Method, Scheme: u.Scheme,
		Host: strings.ToLower(u.Hostname()), Port: portOf(u), Path: truncate(u.EscapedPath(), 512)}
	if req.ContentLength > 0 {
		rec.BytesOut = req.ContentLength
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, t.denied(req, rec, &DeniedError{Host: rec.Host, Port: rec.Port, Reason: ReasonScheme})
	}
	// 先按当前白名单检查主机（连接池里的复用连接不会再次 dial，规则删除后也要立即生效）。
	if rules, perr := t.c.Policy.Rules(req.Context()); perr != nil {
		return nil, t.denied(req, rec, &DeniedError{Host: rec.Host, Port: rec.Port, Reason: ReasonPolicyError})
	} else if ok, why := HostAllowed(rules, rec.Host, rec.Port); !ok {
		return nil, t.denied(req, rec, &DeniedError{Host: rec.Host, Port: rec.Port, Reason: why})
	}
	// 放行日志先落库，写不进去时只有声明了审计 fail-closed 的调用方可以继续（D-38 第 9 条）。
	if t.c.Sink != nil {
		start := rec
		start.Allowed, start.Error = true, "in_progress"
		id, serr := t.c.Sink.Start(context.WithoutCancel(req.Context()), start)
		if serr != nil {
			t.c.logger().Error("egress allow log write failed", "err", serr, "host", rec.Host, "module", rec.Caller.Module,
				"audited_fail_closed", rec.Caller.AuditedFailClosed)
			if !rec.Caller.AuditedFailClosed {
				return nil, t.denied(req, rec, &DeniedError{Host: rec.Host, Port: rec.Port, Reason: ReasonLogFailed})
			}
		}
		rec.ID = id
	}
	h := &hop{}
	resp, err := t.base.RoundTrip(req.WithContext(context.WithValue(req.Context(), hopKey{}, h)))
	h.mu.Lock()
	rec.IP = h.ip
	denied := h.denied
	h.mu.Unlock()
	var de *DeniedError
	if denied == nil && errors.As(err, &de) {
		denied = de
	}
	if denied != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		rec.DurationMS = time.Since(start).Milliseconds()
		return nil, t.denied(req, rec, denied)
	}
	rec.Allowed = true
	if err != nil {
		rec.DurationMS = time.Since(start).Milliseconds()
		rec.Error = errClass(err)
		t.write(req.Context(), rec)
		return nil, err
	}
	rec.Status = resp.StatusCode
	resp.Body = &countBody{rc: resp.Body, done: func(n int64, rerr error) {
		rec.BytesIn = n
		rec.DurationMS = time.Since(start).Milliseconds()
		if rerr != nil {
			rec.Error = errClass(rerr)
		}
		t.write(context.WithoutCancel(req.Context()), rec)
	}}
	return resp, nil
}

// denied 记录拒绝：先写审计（fail-closed，写失败同样拒绝），再写出站日志。
func (t *logTransport) denied(req *http.Request, rec Record, d *DeniedError) error {
	rec.Allowed, rec.Reason = false, d.Reason
	if rec.IP == "" {
		rec.IP = d.IP
	}
	ctx := context.WithoutCancel(req.Context())
	e := audit.Entry{Action: "egress.deny", Phase: "result", Outcome: "denied", ActorID: rec.Caller.UserID,
		ActorEmail: rec.Caller.Email, ResourceType: "egress", ResourceID: fmt.Sprintf("%s:%d", rec.Host, rec.Port),
		Detail: map[string]any{"module": rec.Caller.Module, "method": rec.Method, "scheme": rec.Scheme, "host": rec.Host,
			"port": rec.Port, "path": rec.Path, "ip": rec.IP, "reason": d.Reason}}
	if e.ActorEmail == "" {
		e.ActorEmail = "system"
	}
	if t.c.Audit == nil {
		rec.Error = ReasonAuditFailed
	} else if err := t.c.Audit.Write(ctx, e); err != nil {
		t.c.logger().Error("egress deny audit failed", "err", err)
		rec.Error = ReasonAuditFailed
	}
	t.write(ctx, rec)
	return d
}

func (t *logTransport) write(ctx context.Context, rec Record) {
	if t.c.Sink == nil {
		return
	}
	if err := t.c.Sink.Write(ctx, rec); err != nil {
		t.c.logger().Error("egress log write failed", "err", err, "host", rec.Host)
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// errClass 只记录错误类别，不记录可能带 URL / 参数的原始错误文本。
func errClass(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case strings.Contains(err.Error(), "connection refused"):
		return "connection_refused"
	case strings.Contains(err.Error(), "tls"), strings.Contains(err.Error(), "x509"):
		return "tls_error"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof"
	}
	return "network_error"
}

type countBody struct {
	rc   io.ReadCloser
	n    atomic.Int64
	once sync.Once
	done func(int64, error)
	err  error
}

func (b *countBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.n.Add(int64(n))
	if err != nil && !errors.Is(err, io.EOF) {
		b.err = err
	}
	return n, err
}

func (b *countBody) Close() error {
	err := b.rc.Close()
	b.once.Do(func() { b.done(b.n.Load(), b.err) })
	return err
}

// Check 不发请求，判断 rawURL 当前是否会被放行（新建上游时给管理员提示用）。
func (c *Client) Check(ctx context.Context, rawURL string) (bool, string) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false, ReasonScheme
	}
	rules, err := c.Policy.Rules(ctx)
	if err != nil {
		return false, ReasonPolicyError
	}
	host, port := strings.ToLower(u.Hostname()), portOf(u)
	if a, perr := netip.ParseAddr(host); perr == nil {
		return Decide(rules, host, port, a)
	}
	if ok, why := HostAllowed(rules, host, port); !ok {
		return false, why
	}
	addrs, err := c.resolver().LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return false, ReasonDNS
	}
	for _, a := range addrs {
		if ok, why := Decide(rules, host, port, a); !ok {
			return false, why
		}
	}
	return true, ""
}
