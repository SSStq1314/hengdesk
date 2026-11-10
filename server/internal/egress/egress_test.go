package egress_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/egress"
)

type memSink struct {
	mu        sync.Mutex
	recs      []egress.Record
	failStart bool
	started   int
}

// Start 只计数，不入列表（最终结果由 Write 记录），便于既有断言保持不变。
func (m *memSink) Start(_ context.Context, _ egress.Record) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failStart {
		return 0, errors.New("log down")
	}
	m.started++
	return 0, nil
}

func (m *memSink) Write(_ context.Context, r egress.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, r)
	return nil
}

func (m *memSink) all() []egress.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]egress.Record(nil), m.recs...)
}

type memAudit struct {
	mu   sync.Mutex
	es   []audit.Entry
	fail bool
}

func (m *memAudit) Write(_ context.Context, e audit.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("audit down")
	}
	m.es = append(m.es, e)
	return nil
}

type fakeDNS map[string][]string

func (f fakeDNS) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	ips, ok := f[host]
	if !ok {
		return nil, fmt.Errorf("no such host %s", host)
	}
	var out []netip.Addr
	for _, s := range ips {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

func newClient(p egress.Policy, dns fakeDNS) (*egress.Client, *memSink, *memAudit) {
	s, a := &memSink{}, &memAudit{}
	return &egress.Client{Policy: p, Sink: s, Audit: a, Resolver: dns}, s, a
}

func get(t *testing.T, c *egress.Client, u string) (int, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(egress.WithCaller(context.Background(), egress.Caller{UserID: 7, Email: "a@x.com", Module: "test"}), http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer sk-secret-should-not-be-logged")
	resp, err := c.HTTP().Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func denied(t *testing.T, err error, reason string) {
	t.Helper()
	var de *egress.DeniedError
	if !errors.As(err, &de) {
		t.Fatalf("want DeniedError(%s), got %v", reason, err)
	}
	if de.Reason != reason {
		t.Fatalf("reason=%s want %s", de.Reason, reason)
	}
}

func TestDecide(t *testing.T) {
	rules := egress.MustStatic("host api.deepseek.com 443", "wildcard *.openai.com *", "cidr 10.1.0.0/16 8000,11434", "ip 169.254.169.254 *")
	cases := []struct {
		host string
		port int
		ip   string
		ok   bool
		why  string
	}{
		{"api.deepseek.com", 443, "1.2.3.4", true, ""},
		{"api.deepseek.com", 80, "1.2.3.4", false, egress.ReasonPort},
		{"x.api.deepseek.com", 443, "1.2.3.4", false, egress.ReasonNotAllowed},
		{"a.openai.com", 8443, "1.2.3.5", true, ""},
		{"openai.com", 443, "1.2.3.5", false, egress.ReasonNotAllowed},       // *.x 只匹配子域
		{"api.deepseek.com", 443, "127.0.0.1", false, egress.ReasonInternal}, // 白名单域名解析到内网也拒绝
		{"api.deepseek.com", 443, "10.9.0.1", false, egress.ReasonInternal},
		{"vllm.local", 8000, "10.1.2.3", true, ""},
		{"vllm.local", 22, "10.1.2.3", false, egress.ReasonPort},
		{"x", 80, "169.254.169.254", true, ""}, // 精确 IP 显式放行
		{"x", 80, "100.100.100.200", false, egress.ReasonMetadata},
		{"x", 80, "::ffff:127.0.0.1", false, egress.ReasonInternal},
		{"x", 80, "0.0.0.0", false, egress.ReasonInvalid},
		{"x", 80, "fe80::1", false, egress.ReasonInternal},
		{"x", 80, "100.64.1.1", false, egress.ReasonInternal},
	}
	for _, c := range cases {
		ok, why := egress.Decide(rules, c.host, c.port, netip.MustParseAddr(c.ip))
		if ok != c.ok || why != c.why {
			t.Errorf("%s:%d@%s = %v,%s want %v,%s", c.host, c.port, c.ip, ok, why, c.ok, c.why)
		}
	}
	// 元数据地址即使在放行网段内也拒绝。
	wide := egress.MustStatic("cidr 169.254.0.0/16")
	if ok, why := egress.Decide(wide, "x", 80, netip.MustParseAddr("169.254.169.254")); ok || why != egress.ReasonMetadata {
		t.Fatalf("metadata via cidr: %v %s", ok, why)
	}
	if ok, why := egress.Decide(nil, "api.deepseek.com", 443, netip.MustParseAddr("1.2.3.4")); ok || why != egress.ReasonEmpty {
		t.Fatalf("empty: %v %s", ok, why)
	}
}

func TestParseRule(t *testing.T) {
	bad := [][3]string{{"cidr", "0.0.0.0/0", ""}, {"cidr", "::/0", ""}, {"host", "1.2.3.4", ""}, {"wildcard", "*.com", ""},
		{"wildcard", "a.*.com", ""}, {"host", "a b", ""}, {"ip", "x", ""}, {"host", "a.com", "0"}, {"host", "a.com", "70000"}, {"bogus", "a", ""}}
	for _, b := range bad {
		if _, err := egress.ParseRule(b[0], b[1], b[2]); err == nil {
			t.Errorf("want error for %v", b)
		}
	}
	r, err := egress.ParseRule("cidr", "10.1.2.3/16", " 80, 443")
	if err != nil || r.Value != "10.1.0.0/16" || r.PortsString() != "80,443" || !r.CoversInternal() {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestEmptyAllowlistDeniesAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	c, sink, au := newClient(egress.StaticPolicy(nil), fakeDNS{"api.example.com": {"1.2.3.4"}})
	for _, u := range []string{srv.URL, "https://api.example.com/v1/models"} {
		_, err := get(t, c, u)
		denied(t, err, egress.ReasonEmpty)
	}
	if len(au.es) != 2 || au.es[0].Action != "egress.deny" {
		t.Fatalf("audit: %+v", au.es)
	}
	if r := sink.all(); len(r) != 2 || r[0].Allowed {
		t.Fatalf("log: %+v", r)
	}
}

func TestInternalAndMetadataBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	// 白名单只有公网域名：直连内网 / 元数据 IP 全部拒绝。
	c, _, _ := newClient(egress.MustStatic("host api.example.com"), nil)
	for u, why := range map[string]string{
		srv.URL: egress.ReasonInternal,
		"http://169.254.169.254/latest/meta-data": egress.ReasonMetadata,
		"http://100.100.100.200/":                 egress.ReasonMetadata,
		"http://[::1]:8080/":                      egress.ReasonInternal,
		"http://10.0.0.1/":                        egress.ReasonInternal,
		"http://0.0.0.0/":                         egress.ReasonInvalid,
	} {
		_, err := get(t, c, u)
		denied(t, err, why)
	}
}

func TestDomainResolvingToInternalBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	// 域名在白名单，但 DNS 解析到 127.0.0.1（DNS rebinding / 内网指向）。
	c, sink, _ := newClient(egress.MustStatic("host evil.example.com *"), fakeDNS{
		"evil.example.com": {"127.0.0.1"},
	})
	_, err := get(t, c, "http://evil.example.com:"+port+"/")
	denied(t, err, egress.ReasonInternal)
	if r := sink.all(); len(r) != 1 || r[0].IP != "127.0.0.1" || r[0].Reason != egress.ReasonInternal {
		t.Fatalf("%+v", r)
	}
	// 混合记录：任一地址为内网即整体拒绝。
	c2, _, _ := newClient(egress.MustStatic("host mixed.example.com *"), fakeDNS{"mixed.example.com": {"1.2.3.4", "169.254.169.254"}})
	_, err = get(t, c2, "http://mixed.example.com:"+port+"/")
	denied(t, err, egress.ReasonMetadata)
}

func TestRedirectToInternalBlocked(t *testing.T) {
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "secret") }))
	defer inner.Close()
	hit := 0
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
	}))
	defer outer.Close()
	ou, _ := url.Parse(outer.URL)
	// 只放行 outer 所在的 127.0.0.1:端口；重定向到 inner（同 IP 不同端口）与元数据地址都要在新的一跳被拒绝。
	c, sink, au := newClient(egress.MustStatic("ip 127.0.0.1 "+ou.Port()), nil)
	_, err := get(t, c, outer.URL+"/?to="+url.QueryEscape(inner.URL+"/x"))
	denied(t, err, egress.ReasonPort)
	if hit != 1 {
		t.Fatalf("outer hit %d", hit)
	}
	_, err = get(t, c, outer.URL+"/?to="+url.QueryEscape("http://169.254.169.254/latest/meta-data/"))
	denied(t, err, egress.ReasonMetadata)
	recs := sink.all()
	if len(recs) != 4 || !recs[0].Allowed || recs[0].Status != 302 || recs[0].IP != "127.0.0.1" || recs[1].Allowed {
		t.Fatalf("%+v", recs)
	}
	if len(au.es) != 2 {
		t.Fatalf("audit %+v", au.es)
	}
	for _, r := range recs {
		if strings.Contains(r.Path, "?") || strings.Contains(r.Path, "to=") {
			t.Fatalf("query string logged: %q", r.Path)
		}
	}
}

func TestExplicitInternalAllowAndLogsHaveNoSecrets(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	c, sink, au := newClient(egress.MustStatic("cidr 127.0.0.0/8 *", "host ollama.internal *"), fakeDNS{"ollama.internal": {"127.0.0.1"}})
	code, err := get(t, c, srv.URL+"/v1/models?api_key=sk-in-query")
	if err != nil || code != 200 {
		t.Fatalf("%v", err)
	}
	if _, err := get(t, c, "http://ollama.internal:"+port+"/api/tags"); err != nil {
		t.Fatal(err)
	}
	if gotAuth == "" {
		t.Fatal("auth header should reach upstream")
	}
	recs := sink.all()
	if len(recs) != 2 || !recs[0].Allowed || recs[0].Status != 200 || recs[0].BytesIn != int64(len(`{"data":[]}`)) ||
		recs[0].Caller.Module != "test" || recs[0].Caller.UserID != 7 || recs[0].Path != "/v1/models" {
		t.Fatalf("%+v", recs)
	}
	dump := fmt.Sprintf("%+v %+v", recs, au.es)
	for _, s := range []string{"sk-secret", "sk-in-query", "Bearer", "api_key"} {
		if strings.Contains(dump, s) {
			t.Fatalf("log contains %q: %s", s, dump)
		}
	}
	if len(au.es) != 0 {
		t.Fatalf("allowed requests must not write deny audit: %+v", au.es)
	}
}

func TestAuditFailureStillDenies(t *testing.T) {
	c, sink, au := newClient(egress.MustStatic("host api.example.com"), nil)
	au.fail = true
	_, err := get(t, c, "http://10.0.0.1/")
	denied(t, err, egress.ReasonInternal)
	if r := sink.all(); len(r) != 1 || r[0].Error != egress.ReasonAuditFailed {
		t.Fatalf("%+v", r)
	}
}

type errPolicy struct{}

func (errPolicy) Rules(context.Context) ([]egress.Rule, error) { return nil, errors.New("db down") }

func TestPolicyErrorFailsClosed(t *testing.T) {
	c, _, _ := newClient(errPolicy{}, nil)
	_, err := get(t, c, "https://api.example.com/")
	denied(t, err, egress.ReasonPolicyError)
}

type mutPolicy struct {
	mu sync.Mutex
	r  []egress.Rule
}

func (m *mutPolicy) Rules(context.Context) ([]egress.Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.r, nil
}

func TestRuleRemovalAppliesToPooledConn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	p := &mutPolicy{r: egress.MustStatic("ip 127.0.0.1 *")}
	c, _, _ := newClient(p, nil)
	if _, err := get(t, c, srv.URL); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.r = egress.MustStatic("host other.example.com")
	p.mu.Unlock()
	_, err := get(t, c, srv.URL)
	denied(t, err, egress.ReasonInternal)
}

func TestSchemeAndCheck(t *testing.T) {
	c, _, _ := newClient(egress.MustStatic("host api.example.com 443", "host rebind.example.com"), fakeDNS{
		"api.example.com": {"1.2.3.4"}, "rebind.example.com": {"192.168.1.1"}})
	if ok, _ := c.Check(context.Background(), "https://api.example.com/v1"); !ok {
		t.Fatal("check allowed")
	}
	if ok, why := c.Check(context.Background(), "http://api.example.com/v1"); ok || why != egress.ReasonPort {
		t.Fatal(why)
	}
	if ok, why := c.Check(context.Background(), "https://rebind.example.com"); ok || why != egress.ReasonInternal {
		t.Fatal(why)
	}
	if ok, why := c.Check(context.Background(), "https://new.example.com"); ok || why != egress.ReasonNotAllowed {
		t.Fatal(why)
	}
}

func TestPlanSeed(t *testing.T) {
	plan := egress.PlanSeed(context.Background(), fakeDNS{"api.deepseek.com": {"1.2.3.4"}, "vllm.corp": {"10.0.0.5"}}, map[string]string{
		"https://api.deepseek.com/v1": "模型上游 ds",
		"http://vllm.corp:8000/v1":    "模型上游 vllm",
		"http://169.254.169.254/v1":   "模型上游 bad",
		"http://127.0.0.1:11434/v1":   "模型上游 ollama",
	})
	got := map[string]bool{}
	for _, r := range plan {
		got[r.Kind+" "+r.Value+" "+r.Ports] = r.Private
	}
	want := map[string]bool{"host api.deepseek.com 443": false, "host vllm.corp 8000": false, "ip 10.0.0.5 8000": true, "ip 127.0.0.1 11434": true}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%v", got)
	}
}

// D-38 第 3 条：端口留空只放行 443，「*」任意端口，不能与具体端口混用。
func TestDefaultPortIs443(t *testing.T) {
	rules := egress.MustStatic("host api.example.com", "host any.example.com *")
	for _, c := range []struct {
		host string
		port int
		ok   bool
	}{{"api.example.com", 443, true}, {"api.example.com", 80, false}, {"api.example.com", 8443, false},
		{"any.example.com", 80, true}, {"any.example.com", 8080, true}} {
		if ok, _ := egress.HostAllowed(rules, c.host, c.port); ok != c.ok {
			t.Errorf("%s:%d = %v want %v", c.host, c.port, ok, c.ok)
		}
	}
	r, err := egress.ParseRule("host", "a.example.com", " * ")
	if err != nil || !r.AnyPort || r.PortsString() != "*" {
		t.Fatalf("* 解析错误：%+v %v", r, err)
	}
	if _, err := egress.ParseRule("host", "a.example.com", "443,*"); err == nil {
		t.Fatal("「*」与具体端口混用应报错")
	}
}

// D-38 第 9 条：放行日志写不进去时，未声明审计 fail-closed 的调用方拒绝，声明了的（模型网关）继续。
func TestAllowLogFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c, sink, au := newClient(egress.MustStatic("ip 127.0.0.1 "+u.Port()), nil)
	sink.failStart = true
	do := func(cl egress.Caller) error {
		req, _ := http.NewRequestWithContext(egress.WithCaller(context.Background(), cl), http.MethodGet, srv.URL, nil)
		resp, err := c.HTTP().Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		return err
	}
	// 未声明（默认）：拒绝，且记拒绝审计。
	var de *egress.DeniedError
	if err := do(egress.Caller{Module: "model.test"}); !errors.As(err, &de) || de.Reason != egress.ReasonLogFailed {
		t.Fatalf("未声明的调用方应被拒绝，got %v", err)
	}
	if n := len(au.es); n != 1 || au.es[0].Action != "egress.deny" {
		t.Fatalf("应写一条拒绝审计，got %+v", au.es)
	}
	// 声明了审计 fail-closed：继续出站。
	if err := do(egress.Caller{Module: "model.chat", AuditedFailClosed: true}); err != nil {
		t.Fatalf("已声明审计 fail-closed 的调用方应放行：%v", err)
	}
	// 拒绝的请求审计也写不进去：仍拒绝。
	au.fail = true
	if err := do(egress.Caller{Module: "model.test"}); !errors.As(err, &de) {
		t.Fatalf("审计失败时仍应拒绝，got %v", err)
	}
}

// 同一 host 连续请求会复用连接；每条放行记录都必须有对端 IP（不能只在 dial 时填写）。
func TestAllowLogPeerIPOnReuse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	c, sink, _ := newClient(egress.MustStatic("ip 127.0.0.1 *"), nil)
	reused := 0
	const n = 5
	for i := 0; i < n; i++ {
		ctx := egress.WithCaller(context.Background(), egress.Caller{UserID: 7, Email: "a@x.com", Module: "test"})
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) {
				if info.Reused {
					reused++
				}
			},
		})
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/x", nil)
		resp, err := c.HTTP().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", resp.StatusCode)
		}
	}
	if reused == 0 {
		t.Fatal("期望后续请求复用连接，否则无法覆盖「复用时 IP 为空」")
	}
	recs := sink.all()
	if len(recs) != n {
		t.Fatalf("记录数=%d want %d %+v", len(recs), n, recs)
	}
	for i, r := range recs {
		if !r.Allowed || r.Status != http.StatusOK || r.IP != "127.0.0.1" {
			t.Fatalf("第 %d 条放行记录缺 IP 或不完整：%+v（复用 %d 次）", i, r, reused)
		}
	}
}

// 307 等重定向的每一跳独立过出站策略并独立记日志；跳本身耗时超过 1ms 时 duration_ms > 0（亚毫秒完成记 0 是精度截断，合理）。
func TestRedirectHopsLoggedIndependently(t *testing.T) {
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer inner.Close()
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		http.Redirect(w, r, inner.URL+"/final", http.StatusTemporaryRedirect)
	}))
	defer outer.Close()
	c, sink, au := newClient(egress.MustStatic("cidr 127.0.0.0/8 *"), nil)
	code, err := get(t, c, outer.URL+"/start")
	if err != nil || code != http.StatusOK {
		t.Fatalf("status=%d err=%v", code, err)
	}
	recs := sink.all()
	if len(recs) != 2 {
		t.Fatalf("应独立记录两跳，got %d %+v", len(recs), recs)
	}
	if recs[0].Status != http.StatusTemporaryRedirect || !recs[0].Allowed || recs[0].IP != "127.0.0.1" || recs[0].Path != "/start" {
		t.Fatalf("第一跳（307）%+v", recs[0])
	}
	if recs[1].Status != http.StatusOK || !recs[1].Allowed || recs[1].IP != "127.0.0.1" || recs[1].Path != "/final" {
		t.Fatalf("第二跳（200）%+v", recs[1])
	}
	if recs[0].DurationMS <= 0 || recs[1].DurationMS <= 0 {
		t.Fatalf("每跳耗时应计入（含 307）：%d %d", recs[0].DurationMS, recs[1].DurationMS)
	}
	if len(au.es) != 0 {
		t.Fatalf("放行重定向不应写拒绝审计：%+v", au.es)
	}
}
