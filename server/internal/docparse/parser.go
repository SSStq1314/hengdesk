// Package docparse 文档文本抽取（FR-15 会话文件；沿用 M0 spike 的可插拔接口，见 docs/spikes/pdf-docx.md）。
package docparse

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// BlockKind 区分结构化块类型，便于后续按结构切片（表格整块、标题作为上下文）。
type BlockKind string

const (
	KindHeading   BlockKind = "heading"
	KindParagraph BlockKind = "paragraph"
	KindTable     BlockKind = "table"
	KindPageText  BlockKind = "page_text" // PDF 等无结构来源的整页文本
)

type Block struct {
	Kind  BlockKind
	Text  string     // 段落/标题/整页文本
	Rows  [][]string // 表格
	Page  int        // 1-based 页码/幻灯片号/工作表序号，0 表示未知
	Level int        // 标题级别
}

type Document struct {
	Parser string
	Blocks []Block
	Meta   map[string]string
}

// Parser 每种格式一个实现；注册表按扩展名/MIME 选择，可按优先级降级（如 pdfium 失败退回 ledongthuc）。
type Parser interface {
	Name() string
	Supports(ext string) bool
	Parse(ctx context.Context, r io.ReaderAt, size int64) (*Document, error)
}

type Registry struct{ ps []Parser }

func (r *Registry) Register(p Parser) { r.ps = append(r.ps, p) }

// Parse 依次尝试支持该扩展名的解析器，首个成功者胜出。
func (r *Registry) Parse(ctx context.Context, filename string, ra io.ReaderAt, size int64) (*Document, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	var errs []string
	for _, p := range r.ps {
		if !p.Supports(ext) {
			continue
		}
		d, err := p.Parse(ctx, ra, size)
		if err == nil {
			return d, nil
		}
		errs = append(errs, p.Name()+": "+err.Error())
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("unsupported format %q", ext)
	}
	return nil, fmt.Errorf("all parsers failed: %s", strings.Join(errs, "; "))
}

// PlainText 把文档压平成文本（表格按行，单元格以 " | " 连接），用于评测与朴素切片。
func (d *Document) PlainText() string {
	var b strings.Builder
	for _, bl := range d.Blocks {
		if bl.Kind == KindTable {
			for _, row := range bl.Rows {
				b.WriteString(strings.Join(row, " | "))
				b.WriteByte('\n')
			}
		} else {
			b.WriteString(bl.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
