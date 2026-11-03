package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// User 用户（本地账号；SSO 在 M1 接入）。
type User struct{ ent.Schema }

func (User) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (User) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("org_id", "employee_no").Unique().Annotations(entsql.IndexWhere("employee_no <> ''")),
		index.Fields("oidc_issuer", "oidc_subject").Unique().Annotations(entsql.IndexWhere("oidc_subject <> ''")),
	}
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("email").NotEmpty().Unique(),
		field.String("display_name").Default(""),
		field.String("employee_no").Default("").Comment("工号（CSV 导入 / 外部目录）"),
		field.String("password_hash").Sensitive().Optional(),
		field.Enum("status").Values("active", "disabled").Default("active"),
		field.Int("org_id"),
		field.String("oidc_issuer").Default("").Comment("OIDC 签发方（已关联 SSO 时）"),
		field.String("oidc_subject").Default("").Comment("OIDC sub（已关联 SSO 时）"),
		field.Time("last_login_at").Optional().Nillable(),
	}
}

func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("org", Organization.Type).Ref("users").Field("org_id").Unique().Required(),
		edge.To("memberships", Membership.Type),
	}
}
