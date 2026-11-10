package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UsageHourly 用量按小时预聚合（FR-32）。维度键里 department_id / agent_id 用 0 表示「无」
// （避免 PostgreSQL UNIQUE 对 NULL 不互斥）。权威数据仍是 usage_records；本表可重建。
type UsageHourly struct{ ent.Schema }

func (UsageHourly) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		// bucket_at 该小时的起始时刻（UTC 存储；按服务器时区切桶后再转 UTC）。
		field.Time("bucket_at"),
		field.Int("user_id"),
		field.Int("department_id").Default(0),
		field.Int("model_id"),
		field.Int("agent_id").Default(0),
		field.Int64("calls").Default(0),
		field.Int64("error_calls").Default(0),
		field.Int64("prompt_tokens").Default(0),
		field.Int64("cached_tokens").Default(0),
		field.Int64("completion_tokens").Default(0),
		field.Int64("reasoning_tokens").Default(0),
		field.Int64("total_tokens").Default(0),
		field.Int64("cost_micros").Default(0),
		field.Int64("estimated_calls").Default(0),
		field.Int64("estimated_tokens").Default(0),
		field.Int64("estimated_cost_micros").Default(0),
		field.Int64("unknown_calls").Default(0),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (UsageHourly) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("org_id", "bucket_at", "user_id", "department_id", "model_id", "agent_id").Unique(),
		index.Fields("org_id", "bucket_at"),
		index.Fields("org_id", "department_id", "bucket_at"),
		index.Fields("org_id", "model_id", "bucket_at"),
		index.Fields("org_id", "agent_id", "bucket_at"),
	}
}
