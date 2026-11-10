package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Budget 预算与配额（FR-13 / D-23）。实时计数在 Valkey，权威数据是 usage_record；计数可随时由 usage_record 重建。
type Budget struct{ ent.Schema }

func (Budget) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Budget) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.Enum("scope").Values("org", "dept", "user", "model", "agent").Comment("维度：组织 / 部门（含下级）/ 个人 / 模型 / 智能体"),
		field.Int("scope_id").Default(0).Comment("org 维度固定 0；其余为部门 / 用户 / 模型 ID"),
		field.Enum("period").Values("day", "month"),
		field.Enum("metric").Values("tokens", "cost").Comment("tokens=总 token；cost=金额（微元）"),
		field.Int64("limit_value").Positive(),
		field.Int("threshold_pct").Default(80).Min(1).Max(100),
		field.Enum("action").Values("block", "alert_only").Default("block"),
		field.Bool("enabled").Default(true),
		field.String("note").Default("").MaxLen(200),
		field.Int("created_by"),
	}
}

func (Budget) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("org_id", "scope", "scope_id", "period", "metric").Unique(),
	}
}
