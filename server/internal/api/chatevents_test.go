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
	b, _ = json.Marshal(msgView{Citations: []Citation{{Index: 1, KBID: 2, KBName: "制度库", FileID: 3, FileName: "制度.pdf", Seq: 4, Snippet: "s"}},
		ToolCalls: []ToolCall{{ID: "t1", Tool: "erp.query", Status: ToolCallApproval, ApprovalID: 9}}})
	for _, k := range []string{`"citations":[{"index":1,"kb_id":2,"kb_name":"制度库","file_id":3,"file_name":"制度.pdf","seq":4,"start":0,"end":0,"snippet":"s","score":0}]`, `"tool_calls":[{"id":"t1","tool":"erp.query","status":"approval","approval_id":9}]`} {
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
