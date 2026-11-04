package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UsageRecord 模型调用用量（每次上游调用一行）。供管理后台统计，也是 PR3 预算 / 配额的数据源：
// 按人（user_id）、部门（department_id，调用时的主部门快照）、模型（model_id）汇总 token 与金额。
// estimated=true 表示上游没有返回 usage（或流中断），token 由网关估算，金额同样是估算值。
// usage_unknown=true 表示拿不到任何用量（超时 / 5xx），按 0 记账，界面显示「用量未知」（D-37 第 5 条）。
// 不建外键：用户 / 模型被删除后用量仍需保留用于对账，model_name 为调用时快照。
type UsageRecord struct{ ent.Schema }

func (UsageRecord) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.Int("user_id"),
		field.Int("department_id").Optional().Nillable(),
		field.Int("model_id"),
		field.String("model_name"),
		field.Int("provider_id"),
		field.String("request_id").Default(""),
		field.Enum("purpose").Values("chat", "test", "embedding").Default("chat"),
		field.Bool("stream").Default(false),
		field.Enum("status").Values("ok", "error").Default("ok"),
		field.String("error_code").Default(""),
		field.Int("prompt_tokens").Default(0),
		field.Int("cached_tokens").Default(0),
		field.Int("completion_tokens").Default(0),
		field.Int("reasoning_tokens").Default(0),
		field.Int("total_tokens").Default(0),
		field.Int64("cost_micros").Default(0).Comment("金额，单位微元（1 元 = 1e6）"),
		field.String("currency").Default("CNY"),
		field.Bool("estimated").Default(false),
		field.Bool("usage_unknown").Default(false).Comment("上游未返回用量（超时 / 5xx 等），按 0 记账；对账时需关注"),
		field.Int("latency_ms").Default(0),
		// attempt 同一请求内的第几次尝试（重试 / 故障转移，从 1 开始）；同一 request_id 可有多条。
		field.Int("attempt").Default(1),
		// fallback_from 切换到备用模型时的原模型名（D-39 第 7 条：model_* 记实际使用的模型，并标注「由 A 切换」）。
		field.String("fallback_from").Default(""),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (UsageRecord) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("org_id", "created_at"),
		index.Fields("user_id", "created_at"),
		index.Fields("department_id", "created_at"),
		index.Fields("model_id", "created_at"),
	}
}
