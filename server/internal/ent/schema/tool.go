package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// Tool 工具注册（MCP / 内置）。硬规则：新工具默认禁用，且默认需要审批。
type Tool struct{ ent.Schema }

func (Tool) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Tool) Fields() []ent.Field {
	return []ent.Field{
		field.String("key").NotEmpty().Unique(),
		field.String("name").NotEmpty(),
		field.String("source").Default("mcp"),
		field.Bool("enabled").Default(false).Comment("新工具默认禁用"),
		field.Enum("exec_mode").Values("auto", "require_approval", "deny").Default("require_approval"),
	}
}
