package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/pkg/edition"
)

type fakePro struct{}

func (fakePro) Name() string       { return "demo" }
func (fakePro) Features() []string { return []string{"demo.feature"} }
func (fakePro) Mount(g *echo.Group, h edition.Host) error {
	g.GET("/ping", func(c echo.Context) error {
		if err := h.Audit(c, "pro.demo.ping", "", "", nil); err != nil {
			return err
		}
		return c.String(http.StatusOK, "pong")
	}, h.RequirePerm("org.member.manage"))
	return nil
}

func TestEditionCommunityByDefault(t *testing.T) {
	edition.ResetForTest()
	h, _ := newServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/edition", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated=%d", rec.Code)
	}
	ck := login(t, h, "member@x.local", "password123").Result().Cookies()
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/edition", nil)
	for _, c := range ck {
		req.AddCookie(c)
	}
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"edition":"community"`) {
		t.Fatal(rec.Body.String())
	}
}

func TestEditionExtensionMounted(t *testing.T) {
	edition.ResetForTest()
	edition.Register(fakePro{})
	t.Cleanup(edition.ResetForTest)
	h, _ := newServer(t)
	get := func(email string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/pro/demo/ping", nil)
		if email != "" {
			for _, c := range login(t, h, email, "password123").Result().Cookies() {
				req.AddCookie(c)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := get(""); c != http.StatusUnauthorized {
		t.Fatalf("anon=%d", c)
	}
	if c := get("member@x.local"); c != http.StatusNotFound {
		t.Fatalf("member=%d", c)
	}
	if c := get("admin@x.local"); c != http.StatusOK {
		t.Fatalf("admin=%d", c)
	}
}
