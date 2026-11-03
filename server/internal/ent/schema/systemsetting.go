package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// SystemSetting 系统级键值设置（如主密钥备份确认）。value 为 JSON。
type SystemSetting struct{ ent.Schema }

func (SystemSetting) Fields() []ent.Field {
	return []ent.Field{
		field.String("key").NotEmpty().Unique(),
		field.JSON("value", map[string]any{}),
		field.Int("updated_by").Optional(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
