package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Role 角色。内置角色 builtin=true 不可删除。
type Role struct{ ent.Schema }

func (Role) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Role) Fields() []ent.Field {
	return []ent.Field{
		field.String("key").NotEmpty().Unique().Comment("内置：super_admin / security_auditor / org_admin / dept_admin / member（见 internal/rbac）"),
		field.String("name").NotEmpty(),
		field.Strings("permissions").Default([]string{}),
		field.Bool("builtin").Default(false),
	}
}

func (Role) Edges() []ent.Edge {
	return []ent.Edge{edge.To("memberships", Membership.Type)}
}
