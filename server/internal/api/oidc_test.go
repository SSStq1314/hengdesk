package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/auth/oidctest"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/httpserver"
	"github.com/SSStq1314/hengdesk/server/internal/session"
	"github.com/SSStq1314/hengdesk/server/internal/store"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

type oidcEnv struct {
	srv *httptest.Server
	idp *oidctest.IdP
	c   *ent.Client
}

func newOIDCEnv(t *testing.T, linkBy string, autoCreate bool) *oidcEnv {
	t.Helper()
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	if _, err := store.Seed(ctx, c, store.SeedOptions{OrgName: "o", OrgSlug: "o", AdminEmail: "admin@x.local", AdminPassword: "password123"}); err != nil {
		t.Fatal(err)
	}
	h, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.MinCost)
	org := c.Organization.Query().OnlyX(ctx)
	c.User.Create().SetEmail("member@x.local").SetPasswordHash(string(h)).SetOrg(org).ExecX(ctx)

	idp := oidctest.New("hengdesk", "test-secret")
	t.Cleanup(idp.Close)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	w := audit.EntWriter{C: c}
	e := httpserver.New(slog.Default())
	srv := httptest.NewUnstartedServer(e)
	srv.Start()
	t.Cleanup(srv.Close)
	api.Mount(e, api.Deps{DB: c, Audit: w, Log: slog.Default(),
		Auth: &auth.Service{DB: c, Audit: w, BcryptCost: bcrypt.MinCost,
			Sessions:  &session.Store{R: rdb, TTL: time.Hour},
			Lockout:   &session.Lockout{R: rdb, MaxFailures: 5, Duration: 15 * time.Minute},
			IPLimit:   &session.RateLimiter{R: rdb, Limit: 100, Window: 10 * time.Minute},
			Passwords: auth.PasswordPolicy{MinLen: 8},
			OIDC: &auth.OIDC{R: rdb, Cfg: auth.OIDCConfig{Issuer: idp.URL, ClientID: "hengdesk", ClientSecret: "test-secret",
				RedirectURL: srv.URL + "/api/v1/auth/oidc/callback", LinkBy: linkBy, AutoCreate: autoCreate, OrgSlug: "o"}}},
		System: api.SystemConfig{BackupDir: t.TempDir(), BackupKeep: 5, DiskLowPercent: 10}})
	return &oidcEnv{srv: srv, idp: idp, c: c}
}

func browser(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(r *http.Request, _ []*http.Request) error {
		if r.URL.Path == "/" || r.URL.Path == "/sign-in" {
			return http.ErrUseLastResponse
		}
		return nil
	}}
}

// ssoLogin 走完整的浏览器流程：/auth/oidc/login → IdP → callback，返回最终跳转地址。
func (o *oidcEnv) ssoLogin(t *testing.T, b *http.Client) string {
	t.Helper()
	resp, err := b.Get(o.srv.URL + "/api/v1/auth/oidc/login")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

func lastAudit(t *testing.T, c *ent.Client, action string) *ent.AuditLog {
	t.Helper()
	a, err := c.AuditLog.Query().Where(auditlog.Action(action)).Order(ent.Desc(auditlog.FieldID)).First(context.Background())
	if err != nil {
		t.Fatalf("no audit %s: %v", action, err)
	}
	return a
}

func TestOIDCLinkByEmailLoginLogout(t *testing.T) {
	o := newOIDCEnv(t, "email", false)
	o.idp.SetIdentity(oidctest.Identity{Sub: "sub-1", Email: "Member@x.local", EmailVerified: true, Name: "成员"})
	b := browser(t)
	if loc := o.ssoLogin(t, b); loc != "/" {
		t.Fatalf("location=%q", loc)
	}
	u := o.c.User.Query().Where(user.Email("member@x.local")).OnlyX(context.Background())
	if u.OidcSubject != "sub-1" || u.OidcIssuer != o.idp.URL {
		t.Fatalf("not linked: %+v", u)
	}
	if a := lastAudit(t, o.c, "auth.oidc.link"); a.Outcome != "success" {
		t.Fatal(a.Outcome)
	}
	if a := lastAudit(t, o.c, "auth.login"); a.Outcome != "success" || a.Detail["method"] != "oidc" {
		t.Fatalf("login audit %+v", a)
	}
	resp, _ := b.Get(o.srv.URL + "/api/v1/auth/me")
	var me map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&me)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || me["email"] != "member@x.local" {
		t.Fatalf("me %d %v", resp.StatusCode, me)
	}
	// 登出：带 CSRF，写 auth.logout 审计，会话失效
	su, _ := url.Parse(o.srv.URL)
	var csrf string
	for _, ck := range b.Jar.Cookies(su) {
		if ck.Name == auth.CSRFCookie {
			csrf = ck.Value
		}
	}
	req, _ := http.NewRequest(http.MethodPost, o.srv.URL+"/api/v1/auth/logout", nil)
	req.Header.Set(auth.CSRFHeader, csrf)
	resp, _ = b.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout %d", resp.StatusCode)
	}
	if a := lastAudit(t, o.c, "auth.logout"); a.ActorEmail != "member@x.local" {
		t.Fatalf("logout audit %+v", a)
	}
	resp, _ = b.Get(o.srv.URL + "/api/v1/auth/me")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout %d", resp.StatusCode)
	}
	// 第二次按 sub 命中（即使邮箱变了）
	o.idp.SetIdentity(oidctest.Identity{Sub: "sub-1", Email: "renamed@x.local", EmailVerified: true})
	if loc := o.ssoLogin(t, browser(t)); loc != "/" {
		t.Fatalf("relogin location=%q", loc)
	}
}

func TestOIDCDeniedCases(t *testing.T) {
	o := newOIDCEnv(t, "email", false)
	cases := []struct {
		name   string
		id     oidctest.Identity
		reason string
	}{
		{"unknown_no_autocreate", oidctest.Identity{Sub: "s2", Email: "new@x.local", EmailVerified: true}, "no_local_account"},
		{"unverified_email", oidctest.Identity{Sub: "s3", Email: "member@x.local", EmailVerified: false}, "no_local_account"},
	}
	for _, tc := range cases {
		o.idp.SetIdentity(tc.id)
		if loc := o.ssoLogin(t, browser(t)); loc != "/sign-in?error=sso" {
			t.Fatalf("%s: location=%q", tc.name, loc)
		}
		if a := lastAudit(t, o.c, "auth.login"); a.Outcome != "denied" || a.Detail["reason"] != tc.reason {
			t.Fatalf("%s: audit %+v", tc.name, a.Detail)
		}
	}
	if o.c.User.Query().Where(user.Email("new@x.local")).ExistX(context.Background()) {
		t.Fatal("auto create must be off by default")
	}
	// nonce 不匹配
	o.idp.BadNonce = true
	o.idp.SetIdentity(oidctest.Identity{Sub: "s1", Email: "member@x.local", EmailVerified: true})
	if loc := o.ssoLogin(t, browser(t)); loc != "/sign-in?error=sso" {
		t.Fatalf("nonce: %q", loc)
	}
	if a := lastAudit(t, o.c, "auth.login"); a.Detail["reason"] != "nonce_mismatch" {
		t.Fatalf("nonce audit %+v", a.Detail)
	}
	// 缺少 state cookie（CSRF / 会话固定）
	resp, err := browser(t).Get(o.srv.URL + "/api/v1/auth/oidc/callback?code=x&state=y")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get("Location") != "/sign-in?error=sso" {
		t.Fatal(resp.Header.Get("Location"))
	}
	if a := lastAudit(t, o.c, "auth.login"); a.Detail["reason"] != "state_mismatch" {
		t.Fatalf("state audit %+v", a.Detail)
	}
}

func TestOIDCAutoCreateAndLinkBySub(t *testing.T) {
	o := newOIDCEnv(t, "sub", true)
	ctx := context.Background()
	// LinkBy=sub：同邮箱本地账号不会被自动关联或合并
	o.idp.SetIdentity(oidctest.Identity{Sub: "s1", Email: "member@x.local", EmailVerified: true})
	if loc := o.ssoLogin(t, browser(t)); loc != "/sign-in?error=sso" {
		t.Fatalf("location=%q", loc)
	}
	o.idp.SetIdentity(oidctest.Identity{Sub: "s9", Email: "fresh@x.local", EmailVerified: true, Name: "新人"})
	if loc := o.ssoLogin(t, browser(t)); loc != "/" {
		t.Fatalf("location=%q", loc)
	}
	u := o.c.User.Query().Where(user.Email("fresh@x.local")).WithMemberships(func(q *ent.MembershipQuery) { q.WithRole() }).OnlyX(ctx)
	if u.OidcSubject != "s9" || len(u.Edges.Memberships) != 1 || u.Edges.Memberships[0].Edges.Role.Key != "member" {
		t.Fatalf("auto create %+v", u)
	}
	if a := lastAudit(t, o.c, "user.create"); a.Detail["via"] != "oidc_auto_create" {
		t.Fatalf("%+v", a.Detail)
	}
}

func TestOIDCDisabledByDefault(t *testing.T) {
	h, _ := newServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/config", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatal(rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/login", nil))
	if rec.Code == http.StatusFound {
		t.Fatal("login must not redirect when disabled")
	}
}
