// mockmcp 本地假工单 MCP server（演示剧本第 6、9、10 步与 CI 用）。
//
//	go run ./cmd/mockmcp -addr :18090 [-token xxx]
//
// Streamable HTTP：http://<addr>/mcp；SSE：http://<addr>/sse。
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/mcp/mockmcp"
)

func main() {
	addr := flag.String("addr", ":18090", "监听地址")
	token := flag.String("token", os.Getenv("MOCKMCP_TOKEN"), "非空时要求 Authorization: Bearer <token>")
	flag.Parse()
	m := mockmcp.New(*token)
	slog.Info("mockmcp 已启动", "addr", *addr, "streamable_http", "/mcp", "sse", "/sse")
	srv := &http.Server{Addr: *addr, Handler: m.Handler(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("mockmcp 退出", "err", err)
		os.Exit(1)
	}
}
