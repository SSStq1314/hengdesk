package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
)

// HeaderTraceID 响应头：本次请求的 trace_id（FR-28），审计员可据此回放整条链路。
const HeaderTraceID = "X-Trace-Id"

type traceKey struct{}

type upstreamTraceKey struct{}

// HeaderTraceparent W3C Trace Context 请求头。
const HeaderTraceparent = "traceparent"

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

// WithUpstreamTrace 把受信代理传入的 trace-id 放进 context（D-41 第 1 条）。
func WithUpstreamTrace(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, upstreamTraceKey{}, id)
}

// UpstreamTraceFrom 取 context 中受信代理传入的 trace-id；没有则为空。
func UpstreamTraceFrom(ctx context.Context) string {
	s, _ := ctx.Value(upstreamTraceKey{}).(string)
	return s
}

// ParseTraceparent 解析 W3C traceparent（00-<32hex trace-id>-<16hex parent-id>-<2hex flags>），返回 trace-id。
// 格式不对、版本为 ff 或 trace-id / parent-id 全零时返回 false。
func ParseTraceparent(v string) (string, bool) {
	p := strings.Split(strings.TrimSpace(strings.ToLower(v)), "-")
	if len(p) < 4 || len(p[0]) != 2 || p[0] == "ff" || !isHex(p[0]) || !ValidTraceID(p[1]) ||
		len(p[2]) != 16 || !isHex(p[2]) || len(p[3]) != 2 || !isHex(p[3]) {
		return "", false
	}
	if p[0] == "00" && len(p) != 4 {
		return "", false
	}
	if p[1] == strings.Repeat("0", 32) || p[2] == strings.Repeat("0", 16) {
		return "", false
	}
	return p[1], true
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

func ipIn(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Trace 为每个请求生成服务端 trace_id（FR-28，D-41 第 1 条）：写入请求 context 与 X-Trace-Id 响应头，
// 本次请求产生的所有审计（attempt / result、授权拒绝、对话、出网等）共用它，审计主键始终是它。
// 只有来源 IP（c.RealIP()，已按 HENGDESK_TRUSTED_PROXIES 处理 X-Forwarded-For）在 trusted
// （HENGDESK_TRACE_TRUSTED_PROXIES，默认为空）内时，才接受请求带的 traceparent，
// 其 trace-id 单独记为 upstream_trace_id，仅用于关联排查；其他来源一律忽略。
func Trace(trusted []*net.IPNet) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id := NewTraceID()
			r := c.Request()
			ctx := WithTrace(r.Context(), id)
			if len(trusted) > 0 {
				if tp := r.Header.Get(HeaderTraceparent); tp != "" {
					if ip := peerIP(r.RemoteAddr); ip != nil && ipIn(ip, trusted) {
						if up, ok := ParseTraceparent(tp); ok {
							ctx = WithUpstreamTrace(ctx, up)
						}
					}
				}
			}
			c.SetRequest(r.WithContext(ctx))
			c.Response().Header().Set(HeaderTraceID, id)
			return next(c)
		}
	}
}

// peerIP 取 TCP 对端地址（不读 X-Forwarded-For，避免客户端伪造来源冒充受信代理）。
func peerIP(remote string) net.IP {
	if h, _, err := net.SplitHostPort(remote); err == nil {
		remote = h
	}
	return net.ParseIP(remote)
}
