package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// User 用户（本地账号；SSO 在 M1 接入）。
type User struct{ ent.Schema }

func (User) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("email").NotEmpty().Unique(),
		field.String("display_name").Default(""),
		field.String("password_hash").Sensitive().Optional(),
		field.Enum("status").Values("active", "disabled").Default("active"),
		field.Bool("is_super_admin").Default(false),
		field.Int("org_id"),
		field.Time("last_login_at").Optional().Nillable(),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("org", Organization.Type).Ref("users").Field("org_id").Unique().Required(),
		edge.To("memberships", Membership.Type),
	}
}
