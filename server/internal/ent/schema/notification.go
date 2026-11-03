package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Notification 站内提醒（收件人维度）。目前用于预算阈值告警。
type Notification struct{ ent.Schema }

func (Notification) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.Int("user_id").Comment("收件人"),
		field.String("kind").NotEmpty(),
		field.String("title").NotEmpty(),
		field.JSON("detail", map[string]any{}).Optional(),
		field.String("dedupe_key").NotEmpty().Comment("同一事件只提醒一次：kind+对象+周期+级别"),
		field.Time("read_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (Notification) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "dedupe_key").Unique(),
		index.Fields("user_id", "created_at"),
	}
}
