package usage

import (
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/gateway"
)

// D-37 第 5 条：失败且拿不到用量 → 记 0 并标「用量未知」；流式中断有部分用量 → 按部分计，不算未知。
func TestUnknown(t *testing.T) {
	if !Unknown("upstream_timeout", gateway.Usage{}) {
		t.Fatal("超时无用量应标未知")
	}
	if Unknown("stream_interrupted", gateway.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}) {
		t.Fatal("部分用量不算未知")
	}
	if Unknown("", gateway.Usage{}) {
		t.Fatal("成功调用不标未知")
	}
}
