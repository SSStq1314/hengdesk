package api

// 对话 SSE 事件与消息扩展结构（M1 第三块 PR4，§9.3）。
//
// 本文件只定义接口，不实现：citation 留给第四块（知识库 FR-16–18），tool_call、approval_pending 留给第五块（MCP 工具、审批 FR-22/25）。
// 当前服务端不会发出这三类事件，消息上的 citations / tool_calls 也始终为空（不持久化）。
// 客户端必须忽略不认识的事件名，这样后续模块上线时旧版前端不会出错。

// SSE 事件名。
const (
	EventToken           = "token"            // 增量文本：{"content":"..."}
	EventDone            = "done"             // 生成结束：msgView
	EventError           = "error"            // 生成失败：{"error":{"code","message"}}
	EventCitation        = "citation"         // 预留：Citation，引用到的知识库片段
	EventToolCall        = "tool_call"        // 预留：ToolCall，工具调用开始 / 状态变化
	EventApprovalPending = "approval_pending" // 预留：ApprovalPending，工具调用等待审批
)

// Citation 是一条回复引用的知识库片段（预留）。Index 对应正文中的 [n] 角标。
// 只返回当前用户有权读取的片段；无权时整条不返回，不暴露标题。
type Citation struct {
	Index        int     `json:"index"`
	KBID         int     `json:"kb_id"`
	DocumentID   int     `json:"document_id"`
	DocumentName string  `json:"document_name"`
	ChunkID      string  `json:"chunk_id"`
	Snippet      string  `json:"snippet,omitempty"`
	Page         int     `json:"page,omitempty"`
	Score        float64 `json:"score,omitempty"`
}

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
