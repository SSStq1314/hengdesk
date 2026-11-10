package mockupstream

import "testing"

func TestApplyDemoScriptTicketVsKB(t *testing.T) {
	tools := []any{map[string]any{"type": "function", "function": map[string]any{"name": "create_ticket"}}}
	msgs := []any{map[string]any{"role": "user", "content": "请创建一张报销工单"}}
	call, name, _, args, _ := applyDemoScript(msgs, tools, "user", false, "x", "hello", nil, "")
	if !call || name != "create_ticket" || len(args) == 0 || args[0] == "" {
		t.Fatalf("工单意图应调用 create_ticket：call=%v name=%s args=%v", call, name, args)
	}
	msgs = []any{map[string]any{"role": "user", "content": "普通员工的差旅标准是什么？"}}
	call, _, content, _, _ := applyDemoScript(msgs, tools, "user", true, "create_ticket", "hello", nil, "")
	if call {
		t.Fatalf("知识问题不应调用工具，content=%s", content)
	}
	if content == "" {
		t.Fatal("应返回文本回答")
	}
}
