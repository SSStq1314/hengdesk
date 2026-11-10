// mockoidc 轻量 OIDC IdP，供 M1 演示剧本第 2 步与离线验收使用（非 Keycloak）。
//
//	./mockoidc -addr :18081 -public-url http://mockoidc:18081
//
// 预设财务部 / 市场部演示账号；验收脚本可通过 POST /set-identity 指定下一次登录身份，
// 或在 /authorize 上带 login_hint=<email>。
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/auth/oidctest"
)

func main() {
	addr := flag.String("addr", ":18081", "监听地址")
	public := flag.String("public-url", "", "对外 issuer（发现文档与 iss）。空则从请求 Host 推断，不推荐")
	clientID := flag.String("client-id", "hengdesk-demo", "OAuth client_id")
	secret := flag.String("client-secret", "demo-secret", "OAuth client_secret")
	flag.Parse()
	base := *public
	if base == "" {
		// 回退：调用方应显式传入，避免 issuer 与配置不一致。
		base = "http://127.0.0.1" + portOf(*addr)
		slog.Warn("未指定 -public-url，issuer 回退为 " + base)
	}
	idp := oidctest.NewStandalone(*clientID, *secret, base)
	for _, u := range demoUsers() {
		idp.AddUser(u)
	}
	slog.Info("mockoidc 已启动", "addr", *addr, "issuer", idp.Issuer())
	srv := &http.Server{Addr: *addr, Handler: idp.Handler(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("mockoidc 退出", "err", err)
		os.Exit(1)
	}
}

func portOf(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return addr
	}
	return ":18081"
}

func demoUsers() []oidctest.Identity {
	dept := func(path string) map[string]any { return map[string]any{"department": path} }
	return []oidctest.Identity{
		{Sub: "user-a", Email: "a.finance@hengdesk.local", EmailVerified: true, Name: "员工A", Extra: dept("财务部")},
		{Sub: "user-b", Email: "b.marketing@hengdesk.local", EmailVerified: true, Name: "员工B", Extra: dept("市场部")},
		{Sub: "user-c", Email: "c.approver@hengdesk.local", EmailVerified: true, Name: "审批人C", Extra: dept("财务部")},
		{Sub: "user-d", Email: "d.auditor@hengdesk.local", EmailVerified: true, Name: "审计员D", Extra: dept("财务部")},
		{Sub: "user-m", Email: "m.manager@hengdesk.local", EmailVerified: true, Name: "财务经理", Extra: dept("财务部")},
	}
}
