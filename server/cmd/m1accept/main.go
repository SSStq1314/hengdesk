// m1accept 按 PRD §13 跑完整 15 步 M1 演示剧本，并对 AC-01～AC-16、D-46 相关行为做断言。
// 面向已安装的衡台实例发 HTTP 请求（cookie 会话 + CSRF），不嵌入 server 测试夹具。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

type config struct {
	Base               string
	AdminEmail         string
	AdminPassword      string
	MockMCP            string
	MockOIDC           string
	MockUpstream       string
	MockRedir          string
	RestartCmd         string
	SkipOIDC           bool
	SkipRestart        bool
	BudgetLimit        int64
	DataDir            string
	RequireNoPublicNet bool
	ComposeProject     string
}

func main() {
	var cfg config
	flag.StringVar(&cfg.Base, "base", env("M1ACCEPT_BASE", "http://web"), "衡台 Web 根地址")
	flag.StringVar(&cfg.AdminEmail, "admin-email", env("M1ACCEPT_ADMIN_EMAIL", "admin@hengdesk.local"), "初始管理员邮箱")
	flag.StringVar(&cfg.AdminPassword, "admin-password", env("M1ACCEPT_ADMIN_PASSWORD", ""), "初始管理员密码")
	flag.StringVar(&cfg.MockMCP, "mockmcp", env("M1ACCEPT_MOCKMCP", "http://mockmcp:18090"), "mockmcp 根地址（读 /calls）")
	flag.StringVar(&cfg.MockOIDC, "mockoidc", env("M1ACCEPT_MOCKOIDC", "http://mockoidc:18081"), "mockoidc 根地址")
	flag.StringVar(&cfg.MockUpstream, "mockupstream", env("M1ACCEPT_MOCKUPSTREAM", "http://mockupstream:18080"), "mockupstream 根地址（写入站白名单）")
	flag.StringVar(&cfg.MockRedir, "mockredir", env("M1ACCEPT_MOCKREDIR", "http://mockredir:18082"), "mockredir 根地址（AC-09 越界演示开关）")
	flag.StringVar(&cfg.RestartCmd, "restart-cmd", env("M1ACCEPT_RESTART_CMD", ""), "AC-08：重启 server 的命令；空则跳过 compose 重启")
	flag.BoolVar(&cfg.SkipOIDC, "skip-oidc", envBool("M1ACCEPT_SKIP_OIDC"), "不走 SSO，改为管理员给演示账号设密码后本地登录（仅排障）")
	flag.BoolVar(&cfg.SkipRestart, "skip-restart", envBool("M1ACCEPT_SKIP_RESTART"), "跳过 AC-08 compose 重启")
	flag.Int64Var(&cfg.BudgetLimit, "budget-limit", 80, "财务部月预算 token 上限（演示用小值）")
	flag.StringVar(&cfg.DataDir, "data-dir", env("M1ACCEPT_DATA_DIR", ""), "testdata 目录；空则用内置样例")
	flag.StringVar(&cfg.ComposeProject, "compose-project", env("M1ACCEPT_COMPOSE_PROJECT", "hengdesk"), "compose 项目名（AC-08 经 docker.sock 重启 server 时使用）")
	flag.BoolVar(&cfg.RequireNoPublicNet, "require-no-public-net", envBool("M1ACCEPT_REQUIRE_NO_PUBLIC_NET"), "断言本进程无法访问外网解析器与公网地址（仅 CI/断网模拟层）")
	flag.Parse()
	if cfg.AdminPassword == "" {
		fmt.Fprintln(os.Stderr, "请通过 -admin-password 或 M1ACCEPT_ADMIN_PASSWORD 提供管理员密码")
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "\n验收失败：%v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nM1 端到端演示剧本 15 步全部通过。")
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envBool(k string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
	return v == "1" || v == "true" || v == "yes"
}

func run(cfg config) error {
	if cfg.RequireNoPublicNet {
		if err := probeNoPublicNet(); err != nil {
			return err
		}
		fmt.Println("断网探测通过：验收进程无法访问外网解析器或公网地址")
	}
	s := &script{cfg: cfg, started: time.Now()}
	return s.run()
}
