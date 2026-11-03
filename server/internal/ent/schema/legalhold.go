package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LegalHold 法律保留（D-40 第 2 条）：按全组织（user_id 为空）或按用户设置；处于保留中的数据不被保留期清理。
// 解除只写 released_at / released_by，记录本身保留供审计。
type LegalHold struct{ ent.Schema }

func (LegalHold) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.Int("user_id").Optional().Nillable(),
		field.String("reason").NotEmpty().MaxLen(500),
		field.Int("created_by"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("released_at").Optional().Nillable(),
		field.Int("released_by").Optional().Nillable(),
	}
}

func (LegalHold) Indexes() []ent.Index {
	return []ent.Index{index.Fields("org_id", "released_at")}
}
