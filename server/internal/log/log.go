// Package log 提供基于 slog 的 JSON 结构化日志。
package log

import (
	"log/slog"
	"os"
	"strings"
)

// New 创建 JSON logger 并设为默认。
func New(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	lg := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})).With("app", "hengdesk")
	slog.SetDefault(lg)
	return lg
}
