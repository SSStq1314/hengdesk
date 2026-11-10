package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/SSStq1314/hengdesk/server/internal/rawjson"
)

// Conversation 对话会话（FR-14）。只属于创建人本人：所有读写都按 user_id 过滤，非本人一律 404。
// 删除为软删除（deleted_at），保留消息供审计链路还原（FR-28）；物理删除只经保留期清理（D-40 第 2 条，默认不开启，见 chatretention）。
type Conversation struct{ ent.Schema }

func (Conversation) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.Int("user_id"),
		field.String("title").Default("").MaxLen(200),
		// 最近一次选用的模型（快照 ID；模型删除或失去权限后发消息时重新校验）。
		field.Int("model_id").Optional().Nillable(),
		field.Time("deleted_at").Optional().Nillable(),
		// 本会话选用的知识库（最多 5 个，待确认 8）。每次发消息都按当前权限重新过滤，失去 view 的不再参与检索。
		field.JSON("kb_ids", []int{}).Optional(),
		// tool_ids 本会话选用的工具（第五块 PR4，FR-23）。每次生成时按 authz.ToolAccess 现算，失去权限的工具不提供给模型。
		field.JSON("tool_ids", []int{}).Optional(),
		// agent_id 绑定的智能体（M1 第六块）；非空时模型 / 知识库 / 工具取智能体配置，kb_ids / tool_ids 不使用。
		field.Int("agent_id").Optional().Nillable(),
		// agent_debug 调试会话：使用草稿配置（需要 edit），不影响线上版本。
		field.Bool("agent_debug").Default(false),
	}
}

func (Conversation) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Conversation) Edges() []ent.Edge {
	return []ent.Edge{edge.To("messages", Message.Type)}
}

func (Conversation) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "updated_at")}
}

// Message 会话消息。assistant 消息在开始生成时先落一行（status=streaming），
// 结束后按结果更新为 done / stopped（用户停止或断开，已生成部分照常保存与计量）/ error。
type Message struct{ ent.Schema }

func (Message) Fields() []ent.Field {
	return []ent.Field{
		field.Int("conversation_id"),
		field.Enum("role").Values("user", "assistant"),
		field.Text("content").Default(""),
		field.Enum("status").Values("streaming", "done", "stopped", "error", "awaiting_approval").Default("done"),
		// 实际使用的模型名（快照）；切换到备用模型时 fallback_from 为主模型名（D-39 第 7 条）。
		field.String("model_name").Default(""),
		field.String("fallback_from").Default(""),
		field.String("error_code").Default(""),
		field.String("error").Default("").MaxLen(500),
		field.Int("prompt_tokens").Default(0),
		field.Int("completion_tokens").Default(0),
		// usage_estimated 上游未返回 usage、token 为网关估算值（D-41 第 4 条，界面标「估算」）。
		field.Bool("usage_estimated").Default(false),
		// request_id 关联审计与 usage_records（同一次请求的所有尝试）。
		field.String("request_id").Default(""),
		// superseded_at 被「重新生成 / 重试」替换的旧回复（D-40 第 6 条）：软保留供审计，UI 与后续上下文不再使用。
		field.Time("superseded_at").Optional().Nillable(),
		// citations 本条回复引用的知识库片段（FR-18）。读取时按当前权限再过滤，失去权限的文件整条不返回。
		field.JSON("citations", []MsgCitation{}).Optional(),
		// kb_ungrounded 绑定了知识库但本条回答没有引用任何知识库资料（无命中或用户选择本次不使用知识库，D-42），界面标注「未引用知识库资料」。
		field.Bool("kb_ungrounded").Default(false),
		// tool_calls 本条回复中的工具调用（摘要，界面展示用）；tool_state 等待审批时挂起的工具循环状态（重启后可续跑）。
		field.JSON("tool_calls", []MsgToolCall{}).Optional(),
		field.JSON("tool_state", rawjson.Raw{}).Optional(),
	}
}

// MsgCitation 落库的引用（不含片段全文，只存定位与 200 字摘要）。
type MsgCitation struct {
	Index    int     `json:"index"`
	KBID     int     `json:"kb_id"`
	KBName   string  `json:"kb_name"`
	FileID   int     `json:"file_id"`
	FileName string  `json:"file_name"`
	Seq      int     `json:"seq"`
	Start    int     `json:"start"`
	End      int     `json:"end"`
	Heading  string  `json:"heading,omitempty"`
	Snippet  string  `json:"snippet"`
	Score    float64 `json:"score"`
}

// MsgToolCall 落库的工具调用摘要（参数与结果只存截断后的摘要；完整参数在审批单 / 审计里）。
type MsgToolCall struct {
	ID            string `json:"id"`
	Tool          string `json:"tool"`
	ServerID      int    `json:"server_id,omitempty"`
	Status        string `json:"status"`
	ArgsSummary   string `json:"args_summary,omitempty"`
	ResultSummary string `json:"result_summary,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
	ApprovalID    int    `json:"approval_id,omitempty"`
}

func (Message) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Message) Edges() []ent.Edge {
	return []ent.Edge{edge.From("conversation", Conversation.Type).Ref("messages").Field("conversation_id").Unique().Required()}
}

func (Message) Indexes() []ent.Index {
	return []ent.Index{index.Fields("conversation_id", "id")}
}

// ConversationFile 会话内上传的文件（FR-15）。只在本会话可见，归会话主人所有；原件存对象存储（object_key），
// 抽取的文本按上下文阈值截断后存库（text），发送消息时作为上下文。删除为软删除，原件暂不物理删除（待产品定规则）。
type ConversationFile struct{ ent.Schema }

func (ConversationFile) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.Int("conversation_id"),
		field.Int("user_id"),
		field.String("name").MaxLen(255),
		// 按内容判定的规范类型：txt / md / docx / xlsx / pptx / pdf。
		field.String("kind"),
		field.Int64("size"),
		field.String("sha256"),
		field.String("object_key"),
		field.Text("text").Default(""),
		// 抽取出的原始字符数；超过上下文阈值时 truncated=true，text 只保留前段。
		field.Int("chars").Default(0),
		field.Bool("truncated").Default(false),
		field.Time("deleted_at").Optional().Nillable(),
	}
}

func (ConversationFile) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (ConversationFile) Indexes() []ent.Index {
	return []ent.Index{index.Fields("conversation_id", "id")}
}
