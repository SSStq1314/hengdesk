package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Department 部门，树形结构。
type Department struct{ ent.Schema }

func (Department) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Department) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty(),
		field.String("external_id").Optional().Comment("外部目录（LDAP/企微等）ID"),
		field.Int("org_id"),
		field.Int("parent_id").Optional().Nillable(),
	}
}

func (Department) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("org", Organization.Type).Ref("departments").Field("org_id").Unique().Required(),
		edge.To("children", Department.Type).From("parent").Field("parent_id").Unique(),
		edge.To("memberships", Membership.Type),
	}
}

func (Department) Indexes() []ent.Index {
	return []ent.Index{index.Fields("org_id", "parent_id", "name").Unique()}
}
