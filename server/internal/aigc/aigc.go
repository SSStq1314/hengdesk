// Package aigc 落实 FR-30 / AC-13：人工智能生成合成内容显式与隐式标识。
//
// 对照《人工智能生成合成内容标识办法》与 GB 45438-2025（开放问题 Q4 仍在）：
//   - 交互场景显式：每条 AI 回复角标「AI 生成」（含 AI + 生成要素）
//   - 复制/导出显式：文本末尾附加可配置后缀
//   - 隐式：API 响应头与导出文件元数据，字段名含 AIGC，对齐附录 E 子集
//
// M1 不允许关闭显式标识（办法第九条的「用户申请去标识」列为 P1）。
package aigc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/systemsetting"
)

const (
	SettingKey = "aigc"

	// DefaultBadge 界面角标（文本角标形式，含「AI」）。
	DefaultBadge = "AI 生成"
	// DefaultSuffix 复制/导出时附加的文字显式标识。
	DefaultSuffix = "本内容由人工智能生成（衡台 HengDesk）"
	// DefaultProducer 隐式标识 ContentProducer。
	DefaultProducer = "衡台 HengDesk"
	// DefaultLabel 隐式标识 Label：1 = 属于人工智能生成合成内容（常见枚举，待确认）。
	DefaultLabel = "1"

	// HeaderName API 隐式标识响应头（字段名含 AIGC）。
	HeaderName = "X-HengDesk-AIGC"
)

// Settings 组织级 AIGC 文案（不能关闭标识）。
type Settings struct {
	Badge    string `json:"badge"`
	Suffix   string `json:"suffix"`
	Producer string `json:"producer"`
	Label    string `json:"label"`
}

// Defaults 返回默认设置。
func Defaults() Settings {
	return Settings{Badge: DefaultBadge, Suffix: DefaultSuffix, Producer: DefaultProducer, Label: DefaultLabel}
}

// Normalize 填补空字段为默认值；拒绝「空角标 / 空后缀」以防止实质关闭标识。
func (s Settings) Normalize() Settings {
	d := Defaults()
	if strings.TrimSpace(s.Badge) == "" {
		s.Badge = d.Badge
	}
	if strings.TrimSpace(s.Suffix) == "" {
		s.Suffix = d.Suffix
	}
	if strings.TrimSpace(s.Producer) == "" {
		s.Producer = d.Producer
	}
	if strings.TrimSpace(s.Label) == "" {
		s.Label = d.Label
	}
	return s
}

// Validate 校验：角标须同时暗示 AI 与生成（含「AI」或「人工智能」，且含「生成」或「合成」）。
func (s Settings) Validate() error {
	s = s.Normalize()
	b := s.Badge
	hasAI := strings.Contains(b, "AI") || strings.Contains(b, "人工智能")
	hasGen := strings.Contains(b, "生成") || strings.Contains(b, "合成")
	if !hasAI || !hasGen {
		return fmt.Errorf("显式标识须同时包含「人工智能/AI」与「生成/合成」要素")
	}
	if len([]rune(s.Badge)) > 40 || len([]rune(s.Suffix)) > 200 || len([]rune(s.Producer)) > 80 {
		return fmt.Errorf("文案过长")
	}
	return nil
}

// Load 读取设置；无记录时返回默认值。
func Load(ctx context.Context, c *ent.Client) (Settings, error) {
	st := Defaults()
	s, err := c.SystemSetting.Query().Where(systemsetting.Key(SettingKey)).Only(ctx)
	if ent.IsNotFound(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if v, ok := s.Value["badge"].(string); ok {
		st.Badge = v
	}
	if v, ok := s.Value["suffix"].(string); ok {
		st.Suffix = v
	}
	if v, ok := s.Value["producer"].(string); ok {
		st.Producer = v
	}
	if v, ok := s.Value["label"].(string); ok {
		st.Label = v
	}
	return st.Normalize(), nil
}

// Save 写设置（调用方负责审计）。
func Save(ctx context.Context, c *ent.Client, st Settings, by int) error {
	st = st.Normalize()
	if err := st.Validate(); err != nil {
		return err
	}
	v := map[string]any{"badge": st.Badge, "suffix": st.Suffix, "producer": st.Producer, "label": st.Label}
	n, err := c.SystemSetting.Update().Where(systemsetting.Key(SettingKey)).SetValue(v).SetUpdatedBy(by).Save(ctx)
	if err != nil || n > 0 {
		return err
	}
	return c.SystemSetting.Create().SetKey(SettingKey).SetValue(v).SetUpdatedBy(by).Exec(ctx)
}

// Meta 文件元数据隐式标识（附录 E 子集）。
type Meta struct {
	Label             string `json:"Label"`
	ContentProducer   string `json:"ContentProducer"`
	ProduceID         string `json:"ProduceID"`
	ReservedCode1     string `json:"ReservedCode1,omitempty"`
	ContentPropagator string `json:"ContentPropagator,omitempty"`
	PropagateID       string `json:"PropagateID,omitempty"`
	ReservedCode2     string `json:"ReservedCode2,omitempty"`
}

// BuildMeta 构造隐式标识。produceID 优先用 request_id，否则用 fallback。
func BuildMeta(st Settings, produceID, fallback string) Meta {
	st = st.Normalize()
	id := strings.TrimSpace(produceID)
	if id == "" {
		id = strings.TrimSpace(fallback)
	}
	if id == "" {
		id = fmt.Sprintf("hd-%d", time.Now().UnixNano())
	}
	return Meta{
		Label:           st.Label,
		ContentProducer: st.Producer,
		ProduceID:       id,
	}
}

// HeaderValue 响应头 JSON：{"AIGC":{...}}。
func HeaderValue(m Meta) string {
	b, _ := json.Marshal(map[string]Meta{"AIGC": m})
	return string(b)
}

// AppendSuffix 在正文末尾附加显式文字标识（复制/导出用）。已带后缀则不重复。
func AppendSuffix(content, suffix string) string {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		suffix = DefaultSuffix
	}
	c := strings.TrimRight(content, " \t\r\n")
	if strings.HasSuffix(c, suffix) {
		return c
	}
	if c == "" {
		return suffix
	}
	return c + "\n\n—— " + suffix
}

// MarkdownComment 导出 Markdown 时写入的 HTML 注释元数据。
func MarkdownComment(m Meta) string {
	return fmt.Sprintf("<!-- AIGC %s -->\n", HeaderValue(m))
}
