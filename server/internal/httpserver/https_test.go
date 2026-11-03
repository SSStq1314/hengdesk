package httpserver

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/config"
)

func TestIsHTTPS(t *testing.T) {
	trusted, err := config.ParseCIDRs("172.16.0.0/12")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, remote, xfp string
		tls               bool
		want              bool
	}{
		{"可信代理 https", "172.18.0.5:1234", "https", false, true},
		{"可信代理 http", "172.18.0.5:1234", "http", false, false},
		{"不可信来源伪造", "203.0.113.9:1234", "https", false, false},
		{"无头", "172.18.0.5:1234", "", false, false},
		{"直连 TLS", "203.0.113.9:1234", "", true, true},
		{"多值取第一个", "172.18.0.5:1234", "HTTPS, http", false, true},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xfp != "" {
			r.Header.Set("X-Forwarded-Proto", c.xfp)
		}
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if got := IsHTTPS(r, trusted); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
