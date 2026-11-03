package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Membership 用户-部门-角色 关系。department 为空表示组织级角色。
type Membership struct{ ent.Schema }

func (Membership) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Membership) Fields() []ent.Field {
	return []ent.Field{
		field.Int("user_id"),
		field.Int("role_id"),
		field.Int("department_id").Optional().Nillable(),
		field.Bool("is_primary").Default(false),
	}
}

func (Membership) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).Ref("memberships").Field("user_id").Unique().Required(),
		edge.From("role", Role.Type).Ref("memberships").Field("role_id").Unique().Required(),
		edge.From("department", Department.Type).Ref("memberships").Field("department_id").Unique(),
	}
}

func (Membership) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "role_id", "department_id").Unique()}
}
