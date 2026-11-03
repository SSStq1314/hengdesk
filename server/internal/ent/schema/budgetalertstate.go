package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// BudgetAlertState 预算告警发送状态（D-37 第 11 条）：按「预算 + 周期 + 档位」记一行，有这一行就不再发送。
// 存在数据库而不是 Valkey：计数重建、Valkey 清空或重启后都不会重复提醒。
// 修改预算后若档位变化，高于新档位的行会被清掉，按新档位重新判断。
type BudgetAlertState struct{ ent.Schema }

func (BudgetAlertState) Fields() []ent.Field {
	return []ent.Field{
		field.Int("org_id"),
		field.Int("budget_id"),
		field.String("period_key").NotEmpty(),
		field.Int("level").Comment("告警档位：阈值百分比或 100"),
		field.Int64("used").Default(0).Comment("触发时的已用量"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (BudgetAlertState) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("budget_id", "period_key", "level").Unique(),
	}
}
