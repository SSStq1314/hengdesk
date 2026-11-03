// Package httpserver 组装 Echo HTTP 服务。
package httpserver

import (
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// Version 由构建时注入。
var Version = "dev"

// New 创建带基础中间件和 /healthz 的 Echo 实例。
func New(lg *slog.Logger) *echo.Echo {
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	e.Use(middleware.Recover(), middleware.RequestID())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus: true, LogURI: true, LogMethod: true, LogLatency: true, LogRequestID: true,
		LogValuesFunc: func(_ echo.Context, v middleware.RequestLoggerValues) error {
			lg.Info("http", "method", v.Method, "uri", v.URI, "status", v.Status, "latency_ms", v.Latency.Milliseconds(), "request_id", v.RequestID)
			return nil
		},
	}))
	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok", "version": Version})
	})
	return e
}

// IPExtractor 只有当 TCP 对端位于可信代理网段时才读取 X-Forwarded-For；否则直接用对端地址，防止伪造来源 IP 绕过限速。
func IPExtractor(trusted []*net.IPNet) echo.IPExtractor {
	if len(trusted) == 0 {
		return echo.ExtractIPDirect()
	}
	opts := []echo.TrustOption{echo.TrustLoopback(false), echo.TrustLinkLocal(false), echo.TrustPrivateNet(false)}
	for _, n := range trusted {
		opts = append(opts, echo.TrustIPRange(n))
	}
	return echo.ExtractIPFromXFFHeader(opts...)
}

// IsHTTPS 判断当前请求对用户而言是否走 HTTPS：本身是 TLS 连接，或 TCP 对端属于可信代理且 X-Forwarded-Proto 为 https。
// 不可信来源带的 X-Forwarded-Proto 一律忽略，防止伪造。
func IsHTTPS(r *http.Request, trusted []*net.IPNet) bool {
	if r.TLS != nil {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https") {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
