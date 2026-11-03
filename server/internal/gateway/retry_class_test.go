package gateway_test

// D-39 第 5 条：连接阶段失败可重试 / 切换；读超时不重试。

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/gateway"
)

func TestConnectRefusedRetryable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // 端口已关闭：连接被拒绝
	up := gateway.Upstream{BaseURL: "http://" + addr + "/v1", Kind: "openai_compat", Timeout: 2 * time.Second}
	req := &gateway.ChatRequest{Messages: []gateway.Message{user("hi")}}
	_, e := testClient().Chat(context.Background(), req, up, gateway.ModelConfig{UpstreamModel: "m"})
	if e == nil || e.Code != gateway.CodeUpstreamUnavailable || !e.Retryable || !gateway.ShouldRetry(e, false) {
		t.Fatalf("连接被拒绝应可重试：%+v", e)
	}
}

func TestReadTimeoutNotRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()
	up := gateway.Upstream{BaseURL: srv.URL + "/v1", Kind: "openai_compat", Timeout: 200 * time.Millisecond}
	req := &gateway.ChatRequest{Messages: []gateway.Message{user("hi")}}
	_, e := testClient().Chat(context.Background(), req, up, gateway.ModelConfig{UpstreamModel: "m"})
	if e == nil || e.Code != gateway.CodeUpstreamTimeout || e.Retryable {
		t.Fatalf("读超时不应重试：%+v", e)
	}
}
