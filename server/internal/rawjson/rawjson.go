// Package rawjson 提供一个原样保存的 JSON 字段类型，供 ent schema 使用
// （避免 json.RawMessage 在新版 Go 中被代码生成器展开成 jsontext.Value）。
package rawjson

import "errors"

// Raw 是原样保存的 JSON 文本。
type Raw []byte

// MarshalJSON 原样输出；空值输出 null。
func (r Raw) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}

// UnmarshalJSON 原样保存。
func (r *Raw) UnmarshalJSON(b []byte) error {
	if r == nil {
		return errors.New("rawjson: UnmarshalJSON on nil pointer")
	}
	*r = append((*r)[:0], b...)
	return nil
}
