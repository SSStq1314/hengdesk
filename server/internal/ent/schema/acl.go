package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ACL 资源授权。主体为 user / dept / role；dept 可带 include_children（经 dept_closure 展开）。
// perms 为权限位：1=view 2=use 4=edit 8=manage（高位蕴含低位，写入时已补齐）。
type ACL struct{ ent.Schema }

func (ACL) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "acl"}}
}

func (ACL) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.String("resource_type").NotEmpty(),
		field.String("resource_id").NotEmpty(),
		field.Enum("subject_type").Values("user", "dept", "role"),
		field.Int("subject_id"),
		field.Bool("include_children").Default(false),
		field.Int("perms").Positive(),
		field.Int("created_by").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (ACL) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("resource_type", "resource_id", "subject_type", "subject_id").Unique(),
		index.Fields("org_id", "resource_type", "subject_type", "subject_id"),
	}
}
