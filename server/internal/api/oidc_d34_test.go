package api_test

// FR-03 / AC-02 与 D-34 的端到端测试：真实 HTTP 服务 + 模拟 IdP + cookie jar 走完整浏览器流程。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/auth/oidctest"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/membership"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

type depts struct{ rd, be, def int }

// seedDepts 研发部(RD) / 后端组(RD-BE)，以及默认部门 待分配(PENDING)。
func (o *oidcEnv) seedDepts(t *testing.T) depts {
	t.Helper()
	ctx := context.Background()
	org := o.c.Organization.Query().OnlyX(ctx)
	rd := o.c.Department.Create().SetName("研发部").SetExternalID("RD").SetOrg(org).SaveX(ctx)
	be := o.c.Department.Create().SetName("后端组").SetExternalID("RD-BE").SetOrg(org).SetParentID(rd.ID).SaveX(ctx)
	def := o.c.Department.Create().SetName("待分配").SetExternalID("PENDING").SetOrg(org).SaveX(ctx)
	return depts{rd.ID, be.ID, def.ID}
}

func (o *oidcEnv) addAuditor(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	h, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.MinCost)
	org := o.c.Organization.Query().OnlyX(ctx)
	u := o.c.User.Create().SetEmail("auditor@x.local").SetPasswordHash(string(h)).SetOrg(org).SaveX(ctx)
	r := o.c.Role.Query().Where(role.Key(rbac.SecurityAuditor)).OnlyX(ctx)
	o.c.Membership.Create().SetUser(u).SetRole(r).SetIsPrimary(true).ExecX(ctx)
}

func (o *oidcEnv) memberDepts(t *testing.T, email string) []int {
	t.Helper()
	ctx := context.Background()
	ms := o.c.Membership.Query().Where(membership.HasUserWith(user.Email(email)), membership.HasRoleWith(role.Key(rbac.Member))).AllX(ctx)
	var out []int
	for _, m := range ms {
		if m.DepartmentID != nil {
			out = append(out, *m.DepartmentID)
		}
	}
	slices.Sort(out)
	return out
}

func countAudit(o *oidcEnv, action string) int {
	return o.c.AuditLog.Query().Where(auditlog.Action(action)).CountX(context.Background())
}

// pwLogin 本地密码登录，返回状态码；成功后 b 的 cookie jar 持有会话。
func (o *oidcEnv) pwLogin(t *testing.T, b *http.Client, email string) int {
	t.Helper()
	resp, err := b.Post(o.srv.URL+"/api/v1/auth/login", "application/json",
		bytes.NewBufferString(`{"email":"`+email+`","password":"password123"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// call 带 CSRF 的 API 调用。
func (o *oidcEnv) call(t *testing.T, b *http.Client, method, path, body string) (int, map[string]any) {
	t.Helper()
	su, _ := url.Parse(o.srv.URL)
	var csrf string
	for _, ck := range b.Jar.Cookies(su) {
		if ck.Name == auth.CSRFCookie {
			csrf = ck.Value
		}
	}
	req, _ := http.NewRequest(method, o.srv.URL+path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeader, csrf)
	resp, err := b.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func withJIT(domains ...string) func(*auth.Service) {
	return func(s *auth.Service) { s.OIDC.Cfg.EmailDomains = domains }
}

func withDept(match, def string) func(*auth.Service) {
	return func(s *auth.Service) {
		s.OIDC.Cfg.DeptClaim, s.OIDC.Cfg.DeptMatch, s.OIDC.Cfg.DeptDefault = "department", match, def
	}
}

// AC-02：SSO 首次登录自动建号并按 claim（部门路径）归入部门；每次登录同步，变化才记审计；映射不到归入默认部门。
func TestOIDCDeptMappingByPath(t *testing.T) {
	o := newOIDCEnv(t, "email", true, withJIT("x.local"), withDept("path", "待分配"))
	d := o.seedDepts(t)
	ctx := context.Background()
	id := oidctest.Identity{Sub: "s-new", Email: "new@x.local", EmailVerified: true, Name: "新人",
		Extra: map[string]any{"department": "研发部/后端组"}}
	o.idp.SetIdentity(id)
	if loc := o.ssoLogin(t, browser(t)); loc != "/" {
		t.Fatalf("location=%q", loc)
	}
	u := o.c.User.Query().Where(user.Email("new@x.local")).WithMemberships(func(q *ent.MembershipQuery) { q.WithRole() }).OnlyX(ctx)
	for _, m := range u.Edges.Memberships {
		if m.Edges.Role.Key != rbac.Member {
			t.Fatalf("JIT 只应给成员角色，got %s", m.Edges.Role.Key)
		}
	}
	if got := o.memberDepts(t, "new@x.local"); !slices.Equal(got, []int{d.be}) {
		t.Fatalf("depts=%v", got)
	}
	a := lastAudit(t, o.c, "user.department.sync")
	if a.ActorEmail != "new@x.local" || a.Detail["default_used"] != false {
		t.Fatalf("sync audit %+v", a.Detail)
	}
	n := countAudit(o, "user.department.sync")
	// 部门未变：不再记审计
	if loc := o.ssoLogin(t, browser(t)); loc != "/" {
		t.Fatal(loc)
	}
	if countAudit(o, "user.department.sync") != n {
		t.Fatal("unchanged department must not write audit")
	}
	// 部门变化（且只在 UserInfo 中给出）
	id.Extra, id.ExtraInUserInfo = map[string]any{"department": []any{"研发部"}}, true
	o.idp.SetIdentity(id)
	if loc := o.ssoLogin(t, browser(t)); loc != "/" {
		t.Fatal(loc)
	}
	if got := o.memberDepts(t, "new@x.local"); !slices.Equal(got, []int{d.rd}) {
		t.Fatalf("userinfo depts=%v", got)
	}
	// 映射不到：归入默认部门，审计里带未映射值
	id.Extra, id.ExtraInUserInfo = map[string]any{"department": "市场部"}, false
	o.idp.SetIdentity(id)
	if loc := o.ssoLogin(t, browser(t)); loc != "/" {
		t.Fatal(loc)
	}
	if got := o.memberDepts(t, "new@x.local"); !slices.Equal(got, []int{d.def}) {
		t.Fatalf("default depts=%v", got)
	}
	a = lastAudit(t, o.c, "user.department.sync")
	if a.Detail["default_used"] != true || a.Detail["unmapped"] == nil {
		t.Fatalf("default audit %+v", a.Detail)
	}
}

// 按部门编码（external_id）匹配，claim 为数组时归入多个部门。
func TestOIDCDeptMappingByCode(t *testing.T) {
	o := newOIDCEnv(t, "email", true, withJIT("x.local"), withDept("code", "PENDING"))
	d := o.seedDepts(t)
	o.idp.SetIdentity(oidctest.Identity{Sub: "s-c", Email: "member@x.local", EmailVerified: true,
		Extra: map[string]any{"department": []any{"RD", "RD-BE", "NOPE"}}})
	if loc := o.ssoLogin(t, browser(t)); loc != "/" {
		t.Fatal(loc)
	}
	want := []int{d.rd, d.be}
	slices.Sort(want)
	if got := o.memberDepts(t, "member@x.local"); !slices.Equal(got, want) {
		t.Fatalf("depts=%v want %v", got, want)
	}
}

// D-34：自动建号默认开启，但白名单为空一律不建号；不在白名单的域名也不建号。
func TestOIDCEmailDomainAllowlist(t *testing.T) {
	ctx := context.Background()
	o := newOIDCEnv(t, "email", true) // 白名单为空
	o.idp.SetIdentity(oidctest.Identity{Sub: "s1", Email: "new@x.local", EmailVerified: true})
	if loc := o.ssoLogin(t, browser(t)); loc != "/sign-in?error=sso" {
		t.Fatal(loc)
	}
	if a := lastAudit(t, o.c, "auth.login"); a.Detail["reason"] != "email_domain_not_allowed" {
		t.Fatalf("%+v", a.Detail)
	}
	o = newOIDCEnv(t, "email", true, withJIT("corp.example"))
	o.idp.SetIdentity(oidctest.Identity{Sub: "s2", Email: "new@x.local", EmailVerified: true})
	if loc := o.ssoLogin(t, browser(t)); loc != "/sign-in?error=sso" {
		t.Fatal(loc)
	}
	// 已有本地账号也不能用白名单外的域名按邮箱关联
	o.idp.SetIdentity(oidctest.Identity{Sub: "s3", Email: "member@x.local", EmailVerified: true})
	if loc := o.ssoLogin(t, browser(t)); loc != "/sign-in?error=sso" {
		t.Fatal(loc)
	}
	if o.c.User.Query().Where(user.Email("new@x.local")).ExistX(ctx) {
		t.Fatal("must not create outside allowlist")
	}
	o.idp.SetIdentity(oidctest.Identity{Sub: "s4", Email: "Bob@Corp.Example", EmailVerified: true})
	if loc := o.ssoLogin(t, browser(t)); loc != "/" {
		t.Fatal(loc)
	}
}

// D-34：特权账号不按邮箱自动合并（拒绝 + 审计）；管理员手动绑定（记审计）后可 SSO 登录，解绑后不可。
func TestOIDCPrivilegedManualBind(t *testing.T) {
	ctx := context.Background()
	o := newOIDCEnv(t, "email", true, withJIT("x.local"))
	o.addAuditor(t)
	for _, email := range []string{"admin@x.local", "auditor@x.local"} {
		o.idp.SetIdentity(oidctest.Identity{Sub: "idp-" + email, Email: email, EmailVerified: true})
		if loc := o.ssoLogin(t, browser(t)); loc != "/sign-in?error=sso" {
			t.Fatalf("%s: %q", email, loc)
		}
		if a := lastAudit(t, o.c, "auth.login"); a.Outcome != "denied" || a.Detail["reason"] != "privileged_link_refused" {
			t.Fatalf("%s: %+v", email, a.Detail)
		}
		if u := o.c.User.Query().Where(user.Email(email)).OnlyX(ctx); u.OidcSubject != "" {
			t.Fatalf("%s must not be linked", email)
		}
	}
	admin := browser(t)
	if code := o.pwLogin(t, admin, "admin@x.local"); code != http.StatusOK {
		t.Fatal(code)
	}
	aud := o.c.User.Query().Where(user.Email("auditor@x.local")).OnlyX(ctx)
	path := "/api/v1/admin/users/" + strconv.Itoa(aud.ID) + "/oidc-identity"
	if code, _ := o.call(t, admin, http.MethodPut, path, `{"subject":"aud-sub"}`); code != http.StatusNoContent {
		t.Fatalf("bind %d", code)
	}
	a := lastAudit(t, o.c, "auth.oidc.bind")
	if a.ActorEmail != "admin@x.local" || a.ResourceID != "auditor@x.local" || a.Detail["privileged"] != true || a.Outcome != "success" {
		t.Fatalf("bind audit %+v %+v", a, a.Detail)
	}
	// 同一身份不能再绑给别人
	adm := o.c.User.Query().Where(user.Email("admin@x.local")).OnlyX(ctx)
	if code, _ := o.call(t, admin, http.MethodPut, "/api/v1/admin/users/"+strconv.Itoa(adm.ID)+"/oidc-identity", `{"subject":"aud-sub"}`); code != http.StatusConflict {
		t.Fatalf("dup bind %d", code)
	}
	o.idp.SetIdentity(oidctest.Identity{Sub: "aud-sub", Email: "auditor@x.local", EmailVerified: true})
	if loc := o.ssoLogin(t, browser(t)); loc != "/" {
		t.Fatalf("bound login %q", loc)
	}
	if code, _ := o.call(t, admin, http.MethodDelete, path, ""); code != http.StatusNoContent {
		t.Fatalf("unbind %d", code)
	}
	if lastAudit(t, o.c, "auth.oidc.unbind").ResourceID != "auditor@x.local" {
		t.Fatal("unbind audit")
	}
	if loc := o.ssoLogin(t, browser(t)); loc != "/sign-in?error=sso" {
		t.Fatalf("after unbind %q", loc)
	}
	// 普通成员无权绑定
	m := browser(t)
	if code := o.pwLogin(t, m, "member@x.local"); code != http.StatusOK {
		t.Fatal(code)
	}
	if code, _ := o.call(t, m, http.MethodPut, path, `{"subject":"x"}`); code != http.StatusNotFound {
		t.Fatalf("member bind %d", code)
	}
}

// D-34：强制 SSO 下仅紧急管理员可用密码登录；每次紧急登录记高风险审计，审计员后台看到未读提醒。
func TestOIDCEnforceBreakGlassAndAlerts(t *testing.T) {
	o := newOIDCEnv(t, "email", true, withJIT("x.local"), func(s *auth.Service) {
		s.EnforceSSO, s.BreakGlass = true, []string{"admin@x.local", "auditor@x.local"}
	})
	o.addAuditor(t)
	// 普通成员密码正确也被拒（统一 401），不计入锁定
	if code := o.pwLogin(t, browser(t), "member@x.local"); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	if a := lastAudit(t, o.c, "auth.login"); a.Detail["reason"] != "sso_enforced" {
		t.Fatalf("%+v", a.Detail)
	}
	// 成员仍可走 SSO
	o.idp.SetIdentity(oidctest.Identity{Sub: "m1", Email: "member@x.local", EmailVerified: true})
	if loc := o.ssoLogin(t, browser(t)); loc != "/" {
		t.Fatal(loc)
	}
	// 登录页可得知强制模式
	resp, _ := http.Get(o.srv.URL + "/api/v1/auth/oidc/config")
	var cfg map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&cfg)
	_ = resp.Body.Close()
	if cfg["enforced"] != true {
		t.Fatalf("%+v", cfg)
	}
	before := countAudit(o, auth.AlertAction)
	for range 2 {
		if code := o.pwLogin(t, browser(t), "admin@x.local"); code != http.StatusOK {
			t.Fatal(code)
		}
	}
	if got := countAudit(o, auth.AlertAction); got != before+2 {
		t.Fatalf("alerts %d -> %d", before, got)
	}
	a := lastAudit(t, o.c, auth.AlertAction)
	if a.ActorEmail != "admin@x.local" || a.Detail["severity"] != "high" || a.Detail["kind"] != auth.AlertBreakGlassLogin {
		t.Fatalf("alert %+v", a.Detail)
	}
	if l := lastAudit(t, o.c, "auth.login"); l.Outcome != "success" || l.Detail["break_glass"] != true {
		t.Fatalf("login audit %+v", l.Detail)
	}
	// 审计员：看到未读高风险提醒，标记已读后清零
	aud := browser(t)
	if code := o.pwLogin(t, aud, "auditor@x.local"); code != http.StatusOK {
		t.Fatal(code)
	}
	code, body := o.call(t, aud, http.MethodGet, "/api/v1/admin/alerts", "")
	if code != http.StatusOK || body["unread"].(float64) < 3 {
		t.Fatalf("alerts %d %+v", code, body)
	}
	top := body["items"].([]any)[0].(map[string]any)["id"].(float64)
	if code, _ := o.call(t, aud, http.MethodPost, "/api/v1/admin/alerts/read", `{"up_to_id":`+strconv.Itoa(int(top))+`}`); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if _, body := o.call(t, aud, http.MethodGet, "/api/v1/admin/alerts", ""); body["unread"].(float64) != 0 {
		t.Fatalf("after read %+v", body)
	}
	// 超级管理员（无审计权限）看不到
	adm := browser(t)
	_ = o.pwLogin(t, adm, "admin@x.local")
	if code, _ := o.call(t, adm, http.MethodGet, "/api/v1/admin/alerts", ""); code != http.StatusNotFound {
		t.Fatalf("admin alerts %d", code)
	}
}

type recNotifier struct{ got []auth.Alert }

func (r *recNotifier) Name() string { return "rec" }
func (r *recNotifier) Notify(_ context.Context, a auth.Alert) error {
	r.got = append(r.got, a)
	return nil
}

// 外部通知扩展点：紧急登录时被调用。
func TestBreakGlassNotifierHook(t *testing.T) {
	n := &recNotifier{}
	o := newOIDCEnv(t, "email", false, func(s *auth.Service) {
		s.EnforceSSO, s.BreakGlass, s.Notifiers = true, []string{"admin@x.local"}, []auth.Notifier{n}
	})
	if code := o.pwLogin(t, browser(t), "admin@x.local"); code != http.StatusOK {
		t.Fatal(code)
	}
	if len(n.got) != 1 || n.got[0].Kind != auth.AlertBreakGlassLogin || n.got[0].ActorEmail != "admin@x.local" {
		t.Fatalf("%+v", n.got)
	}
}
