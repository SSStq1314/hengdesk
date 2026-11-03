package api_test

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/httpserver"
	"github.com/SSStq1314/hengdesk/server/internal/store"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/session"
)

type env struct {
	h  http.Handler
	mr *miniredis.Miniredis
	c  *ent.Client
}

func newServer(t *testing.T) (http.Handler, func()) {
	e := newEnv(t)
	return e.h, func() {}
}

func newEnv(t *testing.T) *env {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	if _, err := store.Seed(ctx, c, store.SeedOptions{OrgName: "o", OrgSlug: "o", AdminEmail: "admin@x.local", AdminPassword: "password123"}); err != nil {
		t.Fatal(err)
	}
	h, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.MinCost)
	org := c.Organization.Query().OnlyX(ctx)
	c.User.Create().SetEmail("member@x.local").SetPasswordHash(string(h)).SetOrg(org).ExecX(ctx)
	w := audit.EntWriter{C: c}
	e := httpserver.New(slog.Default())
	// httptest 的对端地址是 192.0.2.1：在测试里把它当作可信反代，以便用 X-Forwarded-For 模拟不同来源。
	e.IPExtractor = httpserver.IPExtractor([]*net.IPNet{{IP: net.IPv4(192, 0, 2, 0), Mask: net.CIDRMask(24, 32)}})
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	api.Mount(e, api.Deps{DB: c, Audit: w, Log: slog.Default(),
		Auth: &auth.Service{DB: c, Audit: w, BcryptCost: bcrypt.MinCost,
			Sessions:  &session.Store{R: rdb, TTL: time.Hour},
			Lockout:   &session.Lockout{R: rdb, MaxFailures: 5, Duration: 15 * time.Minute},
			IPLimit:   &session.RateLimiter{R: rdb, Limit: testIPLimit, Window: 10 * time.Minute},
			Passwords: auth.PasswordPolicy{MinLen: 8}},
		System: api.SystemConfig{BackupDir: t.TempDir(), BackupKeep: 5, DiskLowPercent: 10}})
	return &env{h: e, mr: mr, c: c}
}

const testIPLimit = 40

var ipSeq atomic.Int64

// login 每次用不同的来源 IP，避免触发按 IP 限速；需要固定来源时用 loginFrom。
func login(t *testing.T, h http.Handler, email, pw string) *httptest.ResponseRecorder {
	n := ipSeq.Add(1)
	return loginFrom(t, h, fmt.Sprintf("10.%d.%d.%d", n>>16&255, n>>8&255, n&255), email, pw)
}

func loginFrom(t *testing.T, h http.Handler, ip, email, pw string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"`+email+`","password":"`+pw+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if ip != "" {
		req.Header.Set(echo.HeaderXForwardedFor, ip)
	}
	h.ServeHTTP(rec, req)
	return rec
}

func get(h http.Handler, path string, cookies []*http.Cookie) int {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestLoginFlow(t *testing.T) {
	h, _ := newServer(t)
	if rec := login(t, h, "admin@x.local", "wrong-password"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad pw: %d", rec.Code)
	}
	if rec := login(t, h, "nobody@x.local", "password123"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user: %d", rec.Code)
	}
	rec := login(t, h, "ADMIN@x.local", "password123")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "super_admin") {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	cks := rec.Result().Cookies()
	if get(h, "/api/v1/auth/me", cks) != http.StatusOK {
		t.Fatal("me failed")
	}
	if get(h, "/api/v1/auth/me", nil) != http.StatusUnauthorized {
		t.Fatal("me without cookie should 401")
	}
	// 三员分立：超级管理员不能查看审计日志，按无权限处理返回 404。
	if get(h, "/api/v1/admin/audit-logs", cks) != http.StatusNotFound {
		t.Fatal("super admin must not read audit logs")
	}
}

func TestForbiddenIs404(t *testing.T) {
	h, _ := newServer(t)
	rec := login(t, h, "member@x.local", "password123")
	if rec.Code != http.StatusOK {
		t.Fatalf("member login %d", rec.Code)
	}
	if code := get(h, "/api/v1/admin/audit-logs", rec.Result().Cookies()); code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", code)
	}
}
