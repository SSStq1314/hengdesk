package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Tool 工具注册（MCP / 内置）。硬规则：新工具默认禁用，且默认需要审批。
//
// MCP 工具的 key 为 mcp:<server_id>:<工具名>。风险等级由管理员确认（发现时只给 suggested_risk 建议），
// 是否审批只看平台策略 exec_mode，不看工具自带注解（D-27）。
type Tool struct{ ent.Schema }

func (Tool) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Tool) Fields() []ent.Field {
	return []ent.Field{
		field.String("key").NotEmpty().Unique(),
		field.String("name").NotEmpty(),
		field.String("source").Default("mcp"),
		field.Bool("enabled").Default(false).Comment("新工具默认禁用"),
		field.Enum("exec_mode").Values("auto", "require_approval", "deny").Default("require_approval"),
		field.Int("org_id").Default(0),
		field.Int("server_id").Optional().Nillable(),
		field.String("description").Default(""),
		field.JSON("input_schema", map[string]any{}).Optional(),
		// schema_hash 名称 + 描述 + 入参 schema 的规范化哈希；变化时标记待复核并自动禁用（PRD §9.6）。
		field.String("schema_hash").Default(""),
		field.Enum("risk_level").Values("read_only", "write", "high_risk").Default("high_risk"),
		field.Enum("suggested_risk").Values("read_only", "write", "high_risk").Default("high_risk"),
		// annotations 发现时 MCP 工具注解原样保存（只作展示与建议，不参与审批判断）。
		field.JSON("annotations", map[string]any{}).Optional(),
		// approver_ids 指定审批人（用户 ID）；为空时由有 tool.manage 权限的管理员审批。
		field.Ints("approver_ids").Optional(),
		field.Int("approval_timeout_sec").Default(1800),
		field.Bool("needs_review").Default(false),
		// removed 服务端已不再提供该工具（重新发现时标记，自动禁用）。
		field.Bool("removed").Default(false),
	}
}

func (Tool) Indexes() []ent.Index {
	return []ent.Index{index.Fields("server_id"), index.Fields("org_id")}
}
