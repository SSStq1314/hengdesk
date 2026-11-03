package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"

	"github.com/labstack/echo/v4"
)

// HeaderTraceID 响应头：本次请求的 trace_id（FR-28），审计员可据此回放整条链路。
const HeaderTraceID = "X-Trace-Id"

type traceKey struct{}

var traceRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidTraceID trace_id 格式：32 位小写十六进制（与 W3C trace-id 相同）。
func ValidTraceID(s string) bool { return traceRe.MatchString(s) }

// NewTraceID 生成随机 trace_id。
func NewTraceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// WithTrace 把 trace_id 放进 context；之后经该 context 写的审计自动带上它。
func WithTrace(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceKey{}, id)
}

// TraceFrom 取 context 中的 trace_id。
func TraceFrom(ctx context.Context) string {
	s, _ := ctx.Value(traceKey{}).(string)
	return s
}

// Trace 为每个请求生成服务端 trace_id（FR-28）：写入请求 context 与 X-Trace-Id 响应头，
// 本次请求产生的所有审计（attempt / result、授权拒绝、对话、出网等）共用它。
// 不采用客户端传入的 traceparent，避免伪造或串联他人的链路。
func Trace() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id := NewTraceID()
			r := c.Request()
			c.SetRequest(r.WithContext(WithTrace(r.Context(), id)))
			c.Response().Header().Set(HeaderTraceID, id)
			return next(c)
		}
	}
}
