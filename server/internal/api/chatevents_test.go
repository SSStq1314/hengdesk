package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// 预留结构只定义不实现：消息为空时不应输出 citations / tool_calls，避免前端误判。
func TestReservedChatStructs(t *testing.T) {
	b, _ := json.Marshal(msgView{ID: 1, Role: "assistant", Status: "done"})
	if strings.Contains(string(b), "citations") || strings.Contains(string(b), "tool_calls") {
		t.Fatalf("空预留字段不应序列化：%s", b)
	}
	b, _ = json.Marshal(msgView{Citations: []Citation{{Index: 1, KBID: 2, DocumentID: 3, DocumentName: "制度.pdf", ChunkID: "c1"}},
		ToolCalls: []ToolCall{{ID: "t1", Tool: "erp.query", Status: ToolCallApproval, ApprovalID: 9}}})
	for _, k := range []string{`"citations":[{"index":1,"kb_id":2,"document_id":3,"document_name":"制度.pdf","chunk_id":"c1"}]`, `"tool_calls":[{"id":"t1","tool":"erp.query","status":"approval","approval_id":9}]`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("缺少 %s：%s", k, b)
		}
	}
	for _, e := range []string{EventCitation, EventToolCall, EventApprovalPending} {
		if e == EventToken || e == EventDone || e == EventError {
			t.Fatal("预留事件名不能与现有事件重名")
		}
	}
}
