package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EgressLog 出站日志（AC-09）：每一跳出站一条。不记录请求体、请求头、查询串与密钥。
type EgressLog struct{ ent.Schema }

func (EgressLog) Fields() []ent.Field {
	return []ent.Field{
		field.Time("ts").Default(time.Now).Immutable(),
		field.Int("user_id").Optional().Nillable(),
		field.String("actor_email").Default(""),
		field.String("module").Default(""),
		field.String("method").Default(""),
		field.String("scheme").Default(""),
		field.String("host").Default(""),
		field.Int("port").Default(0),
		field.String("path").Default("").MaxLen(512),
		field.String("ip").Default(""),
		field.Enum("decision").Values("allow", "deny"),
		field.String("reason").Default(""),
		field.Int("status").Default(0),
		field.Int64("duration_ms").Default(0),
		field.Int64("bytes_out").Default(0),
		field.Int64("bytes_in").Default(0),
		field.String("error").Default(""),
	}
}

func (EgressLog) Indexes() []ent.Index {
	return []ent.Index{index.Fields("ts"), index.Fields("decision", "ts"), index.Fields("host", "ts")}
}
