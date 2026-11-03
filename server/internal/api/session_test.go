package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
)

type client struct {
	t    *testing.T
	h    http.Handler
	cks  []*http.Cookie
	csrf string
}

func (e *env) loginAs(t *testing.T, email, pw string) *client {
	t.Helper()
	rec := login(t, e.h, email, pw)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", email, rec.Code, rec.Body)
	}
	var p auth.Principal
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.CSRFToken == "" {
		t.Fatal("no csrf token in login response")
	}
	cl := &client{t: t, h: e.h, cks: rec.Result().Cookies(), csrf: p.CSRFToken}
	var sawSess, sawCSRF bool
	for _, ck := range cl.cks {
		switch ck.Name {
		case auth.CookieName:
			sawSess = ck.HttpOnly
		case auth.CSRFCookie:
			sawCSRF = !ck.HttpOnly && ck.Value == p.CSRFToken
		}
	}
	if !sawSess || !sawCSRF {
		t.Fatalf("cookies wrong: %+v", cl.cks)
	}
	return cl
}

func (cl *client) do(method, path, body string, withCSRF bool) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if withCSRF {
		req.Header.Set(auth.CSRFHeader, cl.csrf)
	}
	for _, ck := range cl.cks {
		req.AddCookie(ck)
	}
	cl.h.ServeHTTP(rec, req)
	// 改密等接口会签发新会话
	if nc := rec.Result().Cookies(); len(nc) > 0 && rec.Code == http.StatusOK {
		var p auth.Principal
		if json.Unmarshal(rec.Body.Bytes(), &p) == nil && p.CSRFToken != "" {
			cl.cks, cl.csrf = nc, p.CSRFToken
		}
	}
	return rec
}

func (cl *client) me() int { return cl.do(http.MethodGet, "/api/v1/auth/me", "", false).Code }

func (e *env) uid(t *testing.T, email string) string {
	u := e.c.User.Query().Where(user.Email(email)).OnlyX(context.Background())
	return strconv.Itoa(u.ID)
}

func TestLockoutAfterFiveFailures(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		if rec := login(t, e.h, "member@x.local", "wrong-password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	// 已锁定：即使密码正确也返回与「密码错误」「账号不存在」完全相同的 401 和提示，不再单独 429。
	locked := login(t, e.h, "member@x.local", "password123")
	wrong := login(t, e.h, "admin@x.local", "wrong-password")
	ghost := login(t, e.h, "nobody@x.local", "wrong-password")
	for name, r := range map[string]*httptest.ResponseRecorder{"locked": locked, "wrong": wrong, "ghost": ghost} {
		if r.Code != http.StatusUnauthorized || r.Body.String() != wrong.Body.String() {
			t.Fatalf("%s: %d %s (want same as wrong: %s)", name, r.Code, r.Body, wrong.Body)
		}
		if r.Header().Get("Retry-After") != "" {
			t.Fatalf("%s leaks Retry-After", name)
		}
	}
	// 不存在的账号同样计数锁定，不暴露账号是否存在
	for i := 0; i < 6; i++ {
		if rec := login(t, e.h, "ghost@x.local", "wrong-password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("ghost %d: %d", i, rec.Code)
		}
	}
	e.mr.FastForward(15*time.Minute + time.Second)
	e.loginAs(t, "member@x.local", "password123")

	// 审计：失败登录记账号、不记密码
	ctx := context.Background()
	rows := e.c.AuditLog.Query().Where(auditlog.Action("auth.login"), auditlog.ActorEmail("member@x.local")).AllX(ctx)
	if len(rows) < 6 {
		t.Fatalf("login audit rows %d", len(rows))
	}
	for _, r := range rows {
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), "wrong-password") || strings.Contains(string(b), "password123") {
			t.Fatal("password leaked into audit log")
		}
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t)
	cl := e.loginAs(t, "member@x.local", "password123")
	if rec := cl.do(http.MethodPost, "/api/v1/auth/logout", "", false); rec.Code != http.StatusForbidden {
		t.Fatalf("logout without csrf: %d", rec.Code)
	}
	if cl.me() != http.StatusOK {
		t.Fatal("session should survive rejected request")
	}
	// 未登录的跨站表单提交
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader("email=a&password=b"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form login: %d", rec.Code)
	}
	if rec := cl.do(http.MethodPost, "/api/v1/auth/logout", "", true); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rec.Code)
	}
	if cl.me() != http.StatusUnauthorized {
		t.Fatal("session still valid after logout")
	}
}

func TestPasswordChangeRevokesAllSessions(t *testing.T) {
	e := newEnv(t)
	a := e.loginAs(t, "member@x.local", "password123")
	b := e.loginAs(t, "member@x.local", "password123")
	if rec := a.do(http.MethodPost, "/api/v1/auth/password", `{"current_password":"bad","new_password":"newpassword1"}`, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong current: %d", rec.Code)
	}
	if rec := a.do(http.MethodPost, "/api/v1/auth/password", `{"current_password":"password123","new_password":"short"}`, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("short pw: %d", rec.Code)
	}
	if rec := a.do(http.MethodPost, "/api/v1/auth/password", `{"current_password":"password123","new_password":"newpassword1"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if b.me() != http.StatusUnauthorized {
		t.Fatal("other session not revoked")
	}
	if a.me() != http.StatusOK {
		t.Fatal("current client should get a fresh session")
	}
	if rec := login(t, e.h, "member@x.local", "password123"); rec.Code != http.StatusUnauthorized {
		t.Fatal("old password still works")
	}
	e.loginAs(t, "member@x.local", "newpassword1")
}

func TestAdminDisableResetAndForceLogout(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs(t, "admin@x.local", "password123")
	id := e.uid(t, "member@x.local")
	base := "/api/v1/admin/users/" + id

	m := e.loginAs(t, "member@x.local", "password123")
	if rec := m.do(http.MethodPost, "/api/v1/admin/users/"+e.uid(t, "admin@x.local")+"/sessions/revoke", "", true); rec.Code != http.StatusNotFound {
		t.Fatalf("member force-logout admin: %d", rec.Code)
	}
	if rec := admin.do(http.MethodPost, base+"/sessions/revoke", "", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"revoked":1`) {
		t.Fatalf("force logout: %d %s", rec.Code, rec.Body)
	}
	if m.me() != http.StatusUnauthorized {
		t.Fatal("force logout ineffective")
	}

	m = e.loginAs(t, "member@x.local", "password123")
	if rec := admin.do(http.MethodPost, base+"/password-reset", `{"new_password":"resetpass99"}`, true); rec.Code != http.StatusNoContent {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	if m.me() != http.StatusUnauthorized {
		t.Fatal("reset did not revoke sessions")
	}

	m = e.loginAs(t, "member@x.local", "resetpass99")
	if rec := admin.do(http.MethodPost, base+"/disable", "", true); rec.Code != http.StatusNoContent {
		t.Fatalf("disable: %d", rec.Code)
	}
	if m.me() != http.StatusUnauthorized {
		t.Fatal("disable did not revoke sessions")
	}
	if rec := login(t, e.h, "member@x.local", "resetpass99"); rec.Code != http.StatusUnauthorized {
		t.Fatal("disabled user can log in")
	}
	if rec := admin.do(http.MethodPost, base+"/enable", "", true); rec.Code != http.StatusNoContent {
		t.Fatal("enable")
	}
	e.loginAs(t, "member@x.local", "resetpass99")

	if rec := admin.do(http.MethodPost, "/api/v1/admin/users/"+e.uid(t, "admin@x.local")+"/disable", "", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("self disable: %d", rec.Code)
	}
	if rec := admin.do(http.MethodPost, "/api/v1/admin/users/999999/disable", "", true); rec.Code != http.StatusNotFound {
		t.Fatalf("missing user: %d", rec.Code)
	}
}

func TestValkeyDownIsFailClosed(t *testing.T) {
	e := newEnv(t)
	cl := e.loginAs(t, "member@x.local", "password123")
	e.mr.Close()
	if code := cl.me(); code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when session store down, got %d", code)
	}
	if rec := login(t, e.h, "member@x.local", "password123"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("login with store down: %d", rec.Code)
	}
}

func TestLoginIPRateLimit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < testIPLimit; i++ {
		// 不同账号，避免账号锁定干扰；同一来源 IP
		if rec := loginFrom(t, e.h, "203.0.113.9", fmt.Sprintf("u%d@x.local", i), "wrong-password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	rec := loginFrom(t, e.h, "203.0.113.9", "member@x.local", "password123")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("want 429 with Retry-After, got %d", rec.Code)
	}
	// 其他来源不受影响
	if rec := loginFrom(t, e.h, "203.0.113.10", "member@x.local", "password123"); rec.Code != http.StatusOK {
		t.Fatalf("other ip: %d", rec.Code)
	}
	// 窗口滑过后恢复
	e.mr.FastForward(10*time.Minute + time.Second)
	if rec := loginFrom(t, e.h, "203.0.113.9", "member@x.local", "password123"); rec.Code != http.StatusOK {
		t.Fatalf("after window: %d", rec.Code)
	}
	n := e.c.AuditLog.Query().Where(auditlog.Action("auth.login"), auditlog.IP("203.0.113.9")).CountX(context.Background())
	if n < testIPLimit+1 {
		t.Fatalf("ip audit rows %d", n)
	}
}

func TestUntrustedPeerCannotSpoofXFF(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < testIPLimit+1; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"a@x.local","password":"wrong-password"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(echo.HeaderXForwardedFor, fmt.Sprintf("198.51.100.%d", i))
		req.RemoteAddr = "198.18.0.1:5555" // 不在可信代理网段：XFF 被忽略，全部算作同一来源
		e.h.ServeHTTP(rec, req)
		if i == testIPLimit && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("spoofed XFF bypassed limit: %d", rec.Code)
		}
	}
}

func TestLoginTimingUnknownAccount(t *testing.T) {
	e := newEnv(t)
	// 未知账号也做一次 bcrypt（DefaultCost 的 dummyHash），耗时不应显著短于真实账号（测试用户为 MinCost，只校验下限）。
	start := time.Now()
	login(t, e.h, "nobody@x.local", "wrong-password")
	if d := time.Since(start); d < 20*time.Millisecond {
		t.Fatalf("unknown account answered in %v: dummy bcrypt skipped?", d)
	}
}

func TestAdminUnlockAudited(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		login(t, e.h, "member@x.local", "wrong-password")
	}
	if rec := login(t, e.h, "member@x.local", "password123"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("should be locked: %d", rec.Code)
	}
	admin := e.loginAs(t, "admin@x.local", "password123")
	if rec := admin.do(http.MethodPost, "/api/v1/admin/users/"+e.uid(t, "member@x.local")+"/unlock", "", true); rec.Code != http.StatusNoContent {
		t.Fatalf("unlock: %d %s", rec.Code, rec.Body)
	}
	e.loginAs(t, "member@x.local", "password123")
	n := e.c.AuditLog.Query().Where(auditlog.Action("auth.unlock"), auditlog.ResourceID(e.uid(t, "member@x.local"))).CountX(context.Background())
	if n != 1 {
		t.Fatalf("unlock audit rows %d", n)
	}
}
