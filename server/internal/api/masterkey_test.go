package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
)

func TestMasterKeyAck(t *testing.T) {
	f := newEnv(t, func(d *api.Deps) {
		d.System.MasterKeyPath, d.System.MasterKeyFingerprint = "/opt/hengdesk/secrets/master.key", "0123456789abcdef"
	})
	admin := f.loginAs(t, "admin@x.local", "password123")
	rec := admin.do(http.MethodGet, "/api/v1/admin/system/status", "", false)
	body := rec.Body.String()
	if !strings.Contains(body, `"configured":true`) || !strings.Contains(body, `"acknowledged":false`) ||
		!strings.Contains(body, "/opt/hengdesk/secrets/master.key") || !strings.Contains(body, "丢失后已存的 Key 无法恢复") {
		t.Fatalf("status %s", body)
	}
	if got := f.loginAs(t, "member@x.local", "password123").do(http.MethodPost, "/api/v1/admin/system/master-key/ack", `{"fingerprint":"0123456789abcdef"}`, true).Code; got != 404 {
		t.Fatalf("member ack %d", got)
	}
	if got := admin.do(http.MethodPost, "/api/v1/admin/system/master-key/ack", `{"fingerprint":"ffff"}`, true).Code; got != 409 {
		t.Fatalf("wrong fp %d", got)
	}
	if rec := admin.do(http.MethodPost, "/api/v1/admin/system/master-key/ack", `{"fingerprint":"0123456789abcdef"}`, true); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"acknowledged":true`) {
		t.Fatalf("ack %d %s", rec.Code, rec.Body)
	}
	if n := f.c.AuditLog.Query().Where(auditlog.Action("system.master_key.ack")).CountX(context.Background()); n != 1 {
		t.Fatalf("audit %d", n)
	}
}

func TestMasterKeyAckInvalidatedByRotation(t *testing.T) {
	f := newEnv(t, func(d *api.Deps) { d.System.MasterKeyFingerprint = "aaaaaaaaaaaaaaaa" })
	admin := f.loginAs(t, "admin@x.local", "password123")
	if got := admin.do(http.MethodPost, "/api/v1/admin/system/master-key/ack", `{"fingerprint":"aaaaaaaaaaaaaaaa"}`, true).Code; got != 200 {
		t.Fatalf("ack %d", got)
	}
	// 直接写一个旧指纹的确认：状态应为未确认
	ctx := context.Background()
	f.c.SystemSetting.Update().SetValue(map[string]any{"fingerprint": "bbbbbbbbbbbbbbbb"}).ExecX(ctx)
	if rec := admin.do(http.MethodGet, "/api/v1/admin/system/status", "", false); !strings.Contains(rec.Body.String(), `"acknowledged":false`) {
		t.Fatalf("rotated fp should need re-ack: %s", rec.Body)
	}
}
