package audit_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

type failWriter struct{}

func (failWriter) Write(context.Context, audit.Entry) error { return errors.New("db down") }

func setup(w audit.Writer, called *bool) *echo.Echo {
	e := echo.New()
	e.Use(audit.Middleware(w, nil, slog.Default()))
	h := func(c echo.Context) error { *called = true; return c.NoContent(http.StatusNoContent) }
	e.POST("/x", h)
	e.GET("/x", h)
	return e
}

func TestFailClosed(t *testing.T) {
	called := false
	e := setup(failWriter{}, &called)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("expected 503 and handler not called, got %d called=%v", rec.Code, called)
	}
}

func TestReadsNotBlocked(t *testing.T) {
	called := false
	e := setup(failWriter{}, &called)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestEntWriterRecordsAttemptAndResult(t *testing.T) {
	c := storetest.NewTestClient(t)
	called := false
	e := setup(audit.EntWriter{C: c}, &called)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("got %d", rec.Code)
	}
	if n := c.AuditLog.Query().CountX(context.Background()); n != 2 {
		t.Fatalf("want 2 audit rows, got %d", n)
	}
}
