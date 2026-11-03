package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Conversation 对话会话（FR-14）。只属于创建人本人：所有读写都按 user_id 过滤，非本人一律 404。
// 删除为软删除（deleted_at），保留消息供审计链路还原（FR-28）；M1 默认不自动删除（保留期待产品确认）。
type Conversation struct{ ent.Schema }

func (Conversation) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.Int("user_id"),
		field.String("title").Default("").MaxLen(200),
		// 最近一次选用的模型（快照 ID；模型删除或失去权限后发消息时重新校验）。
		field.Int("model_id").Optional().Nillable(),
		field.Time("deleted_at").Optional().Nillable(),
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
		field.Enum("status").Values("streaming", "done", "stopped", "error").Default("done"),
		// 实际使用的模型名（快照）；切换到备用模型时 fallback_from 为主模型名（D-39 第 7 条）。
		field.String("model_name").Default(""),
		field.String("fallback_from").Default(""),
		field.String("error_code").Default(""),
		field.String("error").Default("").MaxLen(500),
		field.Int("prompt_tokens").Default(0),
		field.Int("completion_tokens").Default(0),
		// request_id 关联审计与 usage_records（同一次请求的所有尝试）。
		field.String("request_id").Default(""),
	}
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
