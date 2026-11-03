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
		// is_default 默认模型：调用方不指定 model 时使用；全局至多一个（设置时在同一事务里清掉其他模型的标记）。
		field.Bool("is_default").Default(false),
		// 价格表：每百万 token 的价格，单位「微元」（1 元 = 1e6），避免浮点误差。
		field.Int64("price_input_micros").Default(0).Min(0).Comment("未命中缓存的输入，每百万 token"),
		field.Int64("price_cached_micros").Default(0).Min(0).Comment("命中缓存的输入，每百万 token"),
		field.Int64("price_output_micros").Default(0).Min(0).Comment("输出（含思考 token），每百万 token"),
		field.String("currency").Default("CNY").MaxLen(8),
	}
}

func (Model) Edges() []ent.Edge {
	return []ent.Edge{edge.From("provider", ModelProvider.Type).Ref("models").Field("provider_id").Unique().Required()}
}

