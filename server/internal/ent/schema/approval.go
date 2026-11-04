package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Approval 工具调用审批单（FR-25 / FR-28）。args 是发起时的参数快照：审批人看到的就是将要执行的原参数，不可修改。
// 状态：pending → approved / rejected / expired / withdrawn；超时一律按拒绝（D-27）。
// 通过后由 exec_status 的条件更新保证「恰好执行一次」。
type Approval struct{ ent.Schema }

func (Approval) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "approvals"}}
}

func (Approval) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.Int("tool_id"),
		field.String("tool_key").NotEmpty(),
		field.String("tool_name").NotEmpty(),
		field.String("risk_level").Default("high_risk"),
		field.Int("requester_id"),
		field.Int("conversation_id").Optional().Nillable(),
		field.Int("message_id").Optional().Nillable().Comment("挂起的助手消息"),
		field.String("tool_call_id").Default("").MaxLen(200),
		field.JSON("args", map[string]any{}).Optional(),
		field.String("args_hash").Default(""),
		field.String("context_summary").Default("").MaxLen(2000),
		// approver_ids 发起时的审批人快照；为空表示本组织有 tool.manage 权限的管理员。
		field.Ints("approver_ids").Optional(),
		field.Enum("status").Values("pending", "approved", "rejected", "expired", "withdrawn").Default("pending"),
		field.Int("decided_by").Optional().Nillable(),
		field.String("decision_comment").Default("").MaxLen(1000),
		field.Time("decided_at").Optional().Nillable(),
		field.Time("expires_at"),
		// exec_status 通过后的执行：none → running → succeeded / failed（审计不可用时回到 none，可重试）。
		field.Enum("exec_status").Values("none", "running", "succeeded", "failed").Default("none"),
		field.Text("result").Default(""),
		field.Bool("result_is_error").Default(false),
		field.Time("executed_at").Optional().Nillable(),
		field.String("trace_id").Default(""),
	}
}

func (Approval) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Approval) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("org_id", "status", "expires_at"),
		index.Fields("requester_id", "created_at"),
		index.Fields("conversation_id"),
	}
}
