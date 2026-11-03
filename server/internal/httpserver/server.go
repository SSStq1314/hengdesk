// Package httpserver 组装 Echo HTTP 服务。
package httpserver

import (
	"log/slog"
	"net/http"

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
