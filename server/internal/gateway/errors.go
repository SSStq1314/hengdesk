package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// 网关错误码（回给调用方，稳定）。
const (
	CodeBadRequest          = "invalid_request"
	CodeInvalidFunctionName = "invalid_function_name"
	CodeToolChoice          = "tool_choice_unsupported"
	CodeModelNotFound       = "model_not_found"
	CodeUpstreamAuth        = "upstream_auth_failed"
	CodeUpstreamBadRequest  = "upstream_bad_request"
	CodeUpstreamRateLimited = "upstream_rate_limited"
	CodeUpstreamTimeout     = "upstream_timeout"
	CodeUpstreamUnavailable = "upstream_unavailable"
	CodeUpstreamProtocol    = "upstream_protocol_error"
	CodeKeyUnavailable      = "upstream_key_unavailable"
)

// Error 网关错误。Message 已脱敏，可直接回给调用方和写日志。
type Error struct {
	Status         int    `json:"-"`
	Code           string `json:"code"`
	Message        string `json:"message"`
	UpstreamStatus int    `json:"upstream_status,omitempty"`
	UpstreamType   string `json:"upstream_type,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func newErr(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, a...)}
}

// upstreamError 把上游非 2xx 响应映射为网关错误；消息一律脱敏。
func upstreamError(status int, body []byte, secret string) *Error {
	var w struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	msg := ""
	if json.Unmarshal(body, &w) == nil && w.Error.Message != "" {
		msg = w.Error.Message
	} else {
		msg = strings.TrimSpace(string(body))
	}
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	msg = Redact(msg, secret)
	e := &Error{UpstreamStatus: status, UpstreamType: w.Error.Type}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || w.Error.Type == "authentication_error":
		e.Status, e.Code = http.StatusBadGateway, CodeUpstreamAuth
		e.Message = "上游拒绝了网关的凭据，请管理员检查该供应商的 API Key（上游原因：" + msg + "）"
	case status == http.StatusTooManyRequests:
		e.Status, e.Code = http.StatusTooManyRequests, CodeUpstreamRateLimited
		e.Message = "上游限流，请稍后重试（上游原因：" + msg + "）"
	case status >= 500:
		e.Status, e.Code = http.StatusBadGateway, CodeUpstreamUnavailable
		e.Message = fmt.Sprintf("上游服务异常（HTTP %d），请稍后重试", status)
	case strings.Contains(strings.ToLower(msg), "tool_choice"):
		e.Status, e.Code = http.StatusBadRequest, CodeToolChoice
		e.Message = "上游不支持该 tool_choice（思考模式下不能用 required 或指定函数）：" + msg
	default:
		e.Status, e.Code = http.StatusBadRequest, CodeUpstreamBadRequest
		e.Message = fmt.Sprintf("上游拒绝了请求（HTTP %d）：%s", status, msg)
	}
	return e
}
