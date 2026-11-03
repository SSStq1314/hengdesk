package api_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/tlsinfo"
)

func writeSelfSigned(t *testing.T, days int) string {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hd.local"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Duration(days) * 24 * time.Hour), DNSNames: []string{"hd.local"}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "tls.crt")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// D-34 第 3 点：自签证书常驻提示、到期前 30 天提醒，后端提供证书到期信息接口。
func TestSystemTLSInfo(t *testing.T) {
	cert := writeSelfSigned(t, 365)
	var now time.Time
	e := newEnv(t, func(d *api.Deps) {
		d.System.TLSCertFile = cert
		d.System.Now = func() time.Time {
			if now.IsZero() {
				return time.Now()
			}
			return now
		}
	})
	admin := e.loginAs(t, "admin@x.local", "password123")
	get := func(path string, v any) {
		t.Helper()
		rec := admin.do(http.MethodGet, path, "", false)
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), v) != nil {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	var st api.SystemStatus
	get("/api/v1/admin/system/status", &st)
	if st.TLS == nil || !st.TLS.Present || !st.TLS.SelfSigned || st.TLS.Expiring || st.TLS.DaysLeft < 364 {
		t.Fatalf("fresh self-signed: %+v", st.TLS)
	}
	now = time.Now().Add(340 * 24 * time.Hour) // 剩余约 25 天
	var info tlsinfo.Info
	get("/api/v1/admin/system/tls", &info)
	if !info.Expiring || info.Expired || info.DaysLeft > 25 || info.WarnDays != 30 || info.SANs[0] != "hd.local" {
		t.Fatalf("expiring: %+v", info)
	}
	now = time.Now().Add(400 * 24 * time.Hour)
	get("/api/v1/admin/system/tls", &info)
	if !info.Expired {
		t.Fatalf("expired: %+v", info)
	}
	if code := e.loginAs(t, "member@x.local", "password123").do(http.MethodGet, "/api/v1/admin/system/tls", "", false).Code; code != 404 {
		t.Fatalf("member: %d", code)
	}
}

// 未部署证书（默认仅 HTTP）：present=false，不出提示。
func TestSystemTLSAbsent(t *testing.T) {
	e := newEnv(t, func(d *api.Deps) { d.System.TLSCertFile = filepath.Join(t.TempDir(), "none.crt") })
	var st api.SystemStatus
	rec := e.loginAs(t, "admin@x.local", "password123").do(http.MethodGet, "/api/v1/admin/system/status", "", false)
	if json.Unmarshal(rec.Body.Bytes(), &st) != nil || st.TLS == nil || st.TLS.Present {
		t.Fatalf("absent: %s", rec.Body)
	}
}
