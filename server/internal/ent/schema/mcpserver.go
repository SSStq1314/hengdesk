package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// McpServer 远程 MCP 服务器登记（FR-22，P0 只支持 Streamable HTTP / SSE；stdio 为 P1，见 D-03）。
// 出站一律经统一出站客户端（FR-27）；URL 不在白名单时禁止保存。
type McpServer struct{ ent.Schema }

func (McpServer) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "mcp_servers"}}
}

func (McpServer) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.String("name").NotEmpty().MaxLen(100),
		field.String("description").Default("").MaxLen(500),
		field.String("url").NotEmpty().MaxLen(2000),
		field.Enum("transport").Values("streamable_http", "sse").Default("streamable_http"),
		// headers_enc 静态鉴权头（JSON 对象）用主密钥 AES-GCM 加密，AAD 绑定服务器 ID；接口永不回显值。
		field.Bytes("headers_enc").Optional().Nillable().Sensitive(),
		// header_names 只存头名称，用于界面展示「已配置」。
		field.Strings("header_names").Optional(),
		field.Int("timeout_sec").Default(30).Comment("单次会话（发现 / 调用）超时，5–120 秒"),
		field.Bool("enabled").Default(true),
		// 最近一次发现的结果：ok / failed；last_error 为中文提示（不含鉴权头）。
		field.String("last_status").Default(""),
		field.String("last_error").Default("").MaxLen(500),
		field.Time("discovered_at").Optional().Nillable(),
		field.Int("created_by"),
	}
}

func (McpServer) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (McpServer) Indexes() []ent.Index {
	return []ent.Index{index.Fields("org_id", "name").Unique()}
}
