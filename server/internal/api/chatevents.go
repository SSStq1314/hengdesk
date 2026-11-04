package api

import "github.com/SSStq1314/hengdesk/server/internal/ent/schema"

// 对话 SSE 事件与消息扩展结构（M1 第三块 PR4，§9.3）。
//
// citation 自第四块 PR3 起生效：选了知识库的会话，在第一段 token 之前逐条发出引用，并随消息落库。
// tool_call、approval_pending 留给第五块（MCP 工具、审批 FR-22/25），当前不会发出，消息上的 tool_calls 始终为空。
// 客户端必须忽略不认识的事件名，这样后续模块上线时旧版前端不会出错。

// SSE 事件名。
const (
	EventToken           = "token"            // 增量文本：{"content":"..."}
	EventDone            = "done"             // 生成结束：msgView
	EventError           = "error"            // 生成失败：{"error":{"code","message"}}
	EventCitation        = "citation"         // Citation，引用到的知识库片段（在 token 之前发出）
	EventToolCall        = "tool_call"        // 预留：ToolCall，工具调用开始 / 状态变化
	EventApprovalPending = "approval_pending" // 预留：ApprovalPending，工具调用等待审批
)

// Citation 是一条回复引用的知识库片段（第四块 PR3 起生效）。Index 对应正文中的 [n] 角标。
// 只返回当前用户有权读取的片段；无权时整条不返回，不暴露标题。
type Citation = schema.MsgCitation

// 工具调用状态（预留）。
const (
	ToolCallPending   = "pending"  // 已提出，等待执行
	ToolCallApproval  = "approval" // 等待审批（同时发 approval_pending）
	ToolCallRunning   = "running"
	ToolCallSucceeded = "succeeded"
	ToolCallFailed    = "failed"
	ToolCallRejected  = "rejected" // 审批拒绝或策略拦截
)

// ToolCall 是一次工具调用（预留）。参数和结果只给摘要，完整内容按审计规则另存。
type ToolCall struct {
	ID            string `json:"id"`
	Tool          string `json:"tool"`
	ServerID      int    `json:"server_id,omitempty"`
	Status        string `json:"status"`
	ArgsSummary   string `json:"args_summary,omitempty"`
	ResultSummary string `json:"result_summary,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
	ApprovalID    int    `json:"approval_id,omitempty"`
}

// ApprovalPending 通知前端某个工具调用需要审批（预留）。
type ApprovalPending struct {
	ApprovalID int    `json:"approval_id"`
	ToolCallID string `json:"tool_call_id"`
	Tool       string `json:"tool"`
	Reason     string `json:"reason,omitempty"`
}
