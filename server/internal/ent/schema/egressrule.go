package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EgressRule 出站白名单条目（FR-27 / AC-09）。白名单为空即全部拒绝。
//
//	host     精确域名，如 api.deepseek.com
//	wildcard 通配子域名，如 *.example.com（不含 example.com 本身）
//	ip       单个 IP（内网 / 元数据地址只能用 ip / cidr 显式放行）
//	cidr     网段，如 10.0.0.0/8
type EgressRule struct{ ent.Schema }

func (EgressRule) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("kind").Values("host", "wildcard", "ip", "cidr"),
		field.String("value").NotEmpty().MaxLen(255),
		// 允许的端口，逗号分隔；空字符串只放行 443，"*" 表示任意端口（D-38 第 3 条）。
		field.String("ports").Default("").MaxLen(100),
		field.String("note").Default("").MaxLen(200),
		// manual 管理员手工添加；upgrade 升级时从已有模型上游迁移；oidc 来自 OIDC 配置。
		field.Enum("source").Values("manual", "upgrade", "oidc").Default("manual"),
		// 是否放行内网 / 环回 / 元数据地址（由 ip / cidr 是否覆盖内网地址自动计算，仅用于展示与审计）。
		field.Bool("private").Default(false),
		field.Int("created_by").Optional(),
		// 升级 / OIDC 自动添加的条目需管理员复核一次（D-38 第 1 条）；手工条目不需要。
		field.Time("reviewed_at").Optional().Nillable(),
		field.Int("reviewed_by").Optional(),
	}
}

func (EgressRule) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (EgressRule) Indexes() []ent.Index {
	return []ent.Index{index.Fields("kind", "value", "ports").Unique()}
}
