package gateway

import (
	"regexp"
	"strings"
)

var (
	// 常见密钥形态：sk-xxx、Bearer xxx、以及上游回显的 "****abcd" 片段。
	reSKKey   = regexp.MustCompile(`\b(sk|ak|key)-[A-Za-z0-9_\-]{6,}`)
	reBearer  = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{6,}`)
	reMasked  = regexp.MustCompile(`\*{2,}[A-Za-z0-9_\-]{1,16}`)
	reLongHex = regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`)
)

// Redact 去掉文本中的密钥及其片段。secrets 为本次调用实际使用的密钥（完整值与末 4 位都会被替换）。
// 用于上游错误消息：日志和回给调用方的错误都必须先经过这里。
func Redact(s string, secrets ...string) string {
	for _, k := range secrets {
		if k == "" {
			continue
		}
		s = strings.ReplaceAll(s, k, "[REDACTED]")
		if len(k) >= 8 {
			s = strings.ReplaceAll(s, k[len(k)-4:], "[REDACTED]")
			s = strings.ReplaceAll(s, k[:6], "[REDACTED]")
		}
	}
	s = reSKKey.ReplaceAllString(s, "[REDACTED]")
	s = reBearer.ReplaceAllString(s, "Bearer [REDACTED]")
	s = reMasked.ReplaceAllString(s, "[REDACTED]")
	s = reLongHex.ReplaceAllString(s, "[REDACTED]")
	return s
}
