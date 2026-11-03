package docparse

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrEmpty 解析成功但没有可用文本（如扫描件 PDF，需要 OCR，M1 不支持）。
var ErrEmpty = errors.New("empty")

// Extractor 按规范类型选择解析器（PDF：pdfium → ledongthuc 降级）。
type Extractor struct{ reg Registry }

// New 构造默认解析器集合。
func New() *Extractor {
	x := &Extractor{}
	x.reg.Register(&PdfiumPDF{})
	x.reg.Register(LedongthucPDF{})
	x.reg.Register(DocxParser{})
	x.reg.Register(PptxParser{})
	x.reg.Register(XlsxParser{})
	x.reg.Register(TextParser{})
	return x
}

// Extract 返回纯文本（表格按行、单元格以 " | " 连接），最多 maxDocTextRune 字符。
func (x *Extractor) Extract(ctx context.Context, kind string, data []byte) (string, error) {
	d, err := x.reg.Parse(ctx, "f."+kind, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	t := strings.ToValidUTF8(strings.TrimSpace(d.PlainText()), "")
	if t == "" {
		return "", ErrEmpty
	}
	if utf8.RuneCountInString(t) > maxDocTextRune {
		t = string([]rune(t)[:maxDocTextRune])
	}
	return t, nil
}
