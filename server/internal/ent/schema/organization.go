package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Organization 组织（租户根，M0 单组织）。
type Organization struct{ ent.Schema }

func (Organization) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Organization) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("slug").NotEmpty().Unique(),
	}
}

func (Organization) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("departments", Department.Type),
		edge.To("users", User.Type),
	}
}
