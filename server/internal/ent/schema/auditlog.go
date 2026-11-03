package schema

import (
	"context"
	"errors"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ErrAuditImmutable 审计日志只允许追加。
var ErrAuditImmutable = errors.New("audit log is append-only")

// AuditLog 审计日志，append-only：所有字段 Immutable，ent Hook 拒绝 Update/Delete，
// 数据库层另有触发器拒绝 UPDATE/DELETE/TRUNCATE（见 internal/store/migrate.go）。
type AuditLog struct{ ent.Schema }

func (AuditLog) Fields() []ent.Field {
	return []ent.Field{
		field.Time("ts").Default(time.Now).Immutable(),
		field.Int("org_id").Optional().Immutable(),
		field.Int("actor_id").Optional().Immutable(),
		field.String("actor_email").Default("").Immutable(),
		field.String("action").NotEmpty().Immutable().Comment("如 auth.login / http.request"),
		field.String("resource_type").Default("").Immutable(),
		field.String("resource_id").Default("").Immutable(),
		field.Enum("phase").Values("attempt", "result").Default("result").Immutable(),
		field.String("outcome").Default("").Immutable().Comment("success / denied / error / HTTP 状态码"),
		field.String("ip").Default("").Immutable(),
		field.String("user_agent").Default("").Immutable(),
		field.String("request_id").Default("").Immutable(),
		field.String("trace_id").Default("").Immutable(),
		field.JSON("detail", map[string]any{}).Optional().Immutable(),
	}
}

func (AuditLog) Indexes() []ent.Index {
	return []ent.Index{index.Fields("ts"), index.Fields("actor_id", "ts"), index.Fields("trace_id")}
}

func (AuditLog) Hooks() []ent.Hook {
	return []ent.Hook{
		func(next ent.Mutator) ent.Mutator {
			return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
				if !m.Op().Is(ent.OpCreate) {
					return nil, ErrAuditImmutable
				}
				return next.Mutate(ctx, m)
			})
		},
	}
}
