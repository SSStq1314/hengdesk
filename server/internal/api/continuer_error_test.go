package api

import (
	"net/http"
	"testing"
)

func TestParseHTTPErrorMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{
			name:   "预算拦截嵌套 error.message",
			body:   `{"error":{"code":"budget_exceeded","message":"本月部门预算已用尽（已用 6000 tokens / 上限 6000 tokens），将于 2026-12-01 00:00 重置；如需继续使用请联系管理员。"}}`,
			status: http.StatusTooManyRequests,
			want:   "本月部门预算已用尽（已用 6000 tokens / 上限 6000 tokens），将于 2026-12-01 00:00 重置；如需继续使用请联系管理员。",
		},
		{
			name:   "顶层 message 兼容",
			body:   `{"message":"该会话正在生成回复，请等待完成或先停止"}`,
			status: http.StatusConflict,
			want:   "该会话正在生成回复，请等待完成或先停止",
		},
		{
			name:   "空 body 回退状态文本",
			body:   ``,
			status: http.StatusTooManyRequests,
			want:   "Too Many Requests",
		},
		{
			name:   "只有 error.code 没有 message 时回退状态文本",
			body:   `{"error":{"code":"budget_exceeded"}}`,
			status: http.StatusTooManyRequests,
			want:   "Too Many Requests",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseHTTPErrorMessage([]byte(tc.body), tc.status)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
