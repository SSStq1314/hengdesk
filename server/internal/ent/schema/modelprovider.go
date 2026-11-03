package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// ModelProvider 模型供应商（上游）。api_key_enc 为 AES-256-GCM 密文（见 internal/gateway/keyring.go），
// 任何接口都不返回明文或密文。
type ModelProvider struct{ ent.Schema }

func (ModelProvider) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (ModelProvider) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().Unique(),
		// kind 适配器类型：openai_compat（DeepSeek / 通义 / vLLM / Ollama 等）或 anthropic（Messages 协议）。
		field.Enum("kind").Values("openai_compat", "anthropic").Default("openai_compat"),
		// dialect 方言（只对 openai_compat 生效）：deepseek 显式发送 thinking 开关、回传 reasoning_content；
		// dashscope（通义）发送 enable_thinking；vllm 通过 chat_template_kwargs.enable_thinking 开关思考。
		field.Enum("dialect").Values("generic", "deepseek", "dashscope", "vllm").Default("generic"),
		field.String("base_url").NotEmpty(),
		field.Bytes("api_key_enc").Optional().Sensitive(),
		field.Bool("enabled").Default(true),
		// timeout_seconds 非流式整体超时，同时作为流式首包与空闲超时。
		field.Int("timeout_seconds").Default(120).Min(1).Max(3600),
	}
}

func (ModelProvider) Edges() []ent.Edge {
	return []ent.Edge{edge.To("models", Model.Type)}
}
