package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Model 平台内模型。name 为平台内唯一名（调用方使用），upstream_model 为发给上游的模型 ID。
type Model struct{ ent.Schema }

func (Model) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Model) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().Unique(),
		field.Int("provider_id"),
		field.String("upstream_model").NotEmpty(),
		field.Enum("thinking").Values("off", "on", "provider_default").Default("off").
			Comment("思考模式：deepseek 方言下显式发送 thinking 开关"),
		field.Enum("tool_choice_policy").Values("downgrade", "reject").Default("downgrade").
			Comment("思考模式不支持 required / 指定函数时：downgrade 降级为 auto；reject 直接报错"),
		field.Int("context_window").Default(0),
		field.Int("max_output_tokens").Default(0),
		field.Bool("enabled").Default(true),
	}
}

func (Model) Edges() []ent.Edge {
	return []ent.Edge{edge.From("provider", ModelProvider.Type).Ref("models").Field("provider_id").Unique().Required()}
}
