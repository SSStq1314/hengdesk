package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DeptClosure 部门闭包表：每对（祖先, 后代）一行，含自身（depth=0）。
// 用于「部门（含下级）」授权的继承展开，由 org 服务在部门增删移动时维护。
type DeptClosure struct{ ent.Schema }

func (DeptClosure) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "dept_closure"}}
}

func (DeptClosure) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.Int("ancestor_id"),
		field.Int("descendant_id"),
		field.Int("depth").NonNegative(),
	}
}

func (DeptClosure) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("ancestor_id", "descendant_id").Unique(),
		index.Fields("descendant_id"),
	}
}
