package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Agent 智能体（M1 第六块，FR-20）。本表保存「草稿」配置；每次发布在 agent_versions 写一份不可变快照，
// 员工对话只使用最新发布版本（published_version）。访问控制全部走 acl 表（resource_type=agent）：
// 未发布时只有拥有 edit 的人可见（§9.5）。删除为软删除，保留版本与运行记录供审计追溯。
type Agent struct{ ent.Schema }

func (Agent) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Agent) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.String("name").NotEmpty().MaxLen(100),
		field.String("description").Default("").MaxLen(500),
		field.Text("system_prompt").Default(""),
		// opening 开场白：员工新建该智能体的会话时显示（不发送给模型、不计费）。
		field.String("opening").Default("").MaxLen(1000),
		field.Int("model_id").Optional().Nillable(),
		field.JSON("kb_ids", []int{}).Optional(),
		field.JSON("tool_ids", []int{}).Optional(),
		// max_steps 一条回复内最多几轮工具调用（FR-21，1–10，默认 5）。
		field.Int("max_steps").Default(5),
		// published_version 最新发布的版本号；0 表示从未发布（只对拥有 edit 的人可见）。
		field.Int("published_version").Default(0),
		// draft_dirty 草稿相对最新发布版本有未发布的改动（界面提示「有未发布的修改」）。
		field.Bool("draft_dirty").Default(true),
		field.Int("created_by"),
		field.Time("deleted_at").Optional().Nillable(),
	}
}

func (Agent) Indexes() []ent.Index {
	return []ent.Index{index.Fields("org_id", "deleted_at")}
}

// AgentConfig 智能体运行配置（草稿与发布快照共用同一结构）。
type AgentConfig struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	SystemPrompt string `json:"system_prompt"`
	Opening      string `json:"opening"`
	ModelID      *int   `json:"model_id"`
	KBIDs        []int  `json:"kb_ids"`
	ToolIDs      []int  `json:"tool_ids"`
	MaxSteps     int    `json:"max_steps"`
}

// AgentVersion 发布快照（不可变）。版本号在同一智能体内从 1 递增。
type AgentVersion struct{ ent.Schema }

func (AgentVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.Int("agent_id"),
		field.Int("version"),
		field.JSON("config", AgentConfig{}),
		field.String("note").Default("").MaxLen(200),
		field.Int("published_by"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (AgentVersion) Indexes() []ent.Index {
	return []ent.Index{index.Fields("agent_id", "version").Unique()}
}
