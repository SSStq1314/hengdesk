package docparse

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/ledongthuc/pdf"
	"github.com/xuri/excelize/v2"
)

// xlsx 上限：excelize 的超大行号 DoS（GO-2026-6685）之外再加一层保护，只读前 N 行、每行前 M 列。
const (
	maxXlsxRows  = 20000
	maxXlsxCells = 200
)

// XlsxParser 使用 excelize（BSD-3-Clause），每个工作表一个表格块；用 Rows 迭代器并限制行数、列数。
type XlsxParser struct{}

func (XlsxParser) Name() string           { return "xlsx-excelize" }
func (XlsxParser) Supports(e string) bool { return e == "xlsx" }
func (XlsxParser) Parse(ctx context.Context, r io.ReaderAt, size int64) (*Document, error) {
	f, err := excelize.OpenReader(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	doc := &Document{Parser: "xlsx-excelize"}
	for i, sh := range f.GetSheetList() {
		it, err := f.Rows(sh)
		if err != nil {
			return nil, err
		}
		var rows [][]string
		for n := 0; it.Next() && n < maxXlsxRows; n++ {
			if ctx.Err() != nil {
				_ = it.Close()
				return nil, ctx.Err()
			}
			cols, err := it.Columns()
			if err != nil {
				_ = it.Close()
				return nil, err
			}
			if len(cols) > maxXlsxCells {
				cols = cols[:maxXlsxCells]
			}
			rows = append(rows, cols)
		}
		_ = it.Close()
		doc.Blocks = append(doc.Blocks, Block{Kind: KindHeading, Text: sh, Page: i + 1})
		doc.Blocks = append(doc.Blocks, Block{Kind: KindTable, Rows: rows, Page: i + 1})
	}
	return doc, nil
}

// TextParser 处理 md/txt（md 原样保留）。
type TextParser struct{}

func (TextParser) Name() string           { return "text" }
func (TextParser) Supports(e string) bool { return e == "md" || e == "txt" }
func (TextParser) Parse(ctx context.Context, r io.ReaderAt, size int64) (*Document, error) {
	b, err := io.ReadAll(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, err
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	doc := &Document{Parser: "text"}
	for _, p := range strings.Split(string(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))), "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			doc.Blocks = append(doc.Blocks, Block{Kind: KindParagraph, Text: p})
		}
	}
	return doc, nil
}

// LedongthucPDF 纯 Go（BSD-3-Clause），GetPlainText 模式；作为 pdfium 失败时的降级（spike：表格行会被打散）。
type LedongthucPDF struct{}

func (LedongthucPDF) Name() string           { return "pdf-ledongthuc-plain" }
func (LedongthucPDF) Supports(e string) bool { return e == "pdf" }
func (p LedongthucPDF) Parse(ctx context.Context, r io.ReaderAt, size int64) (doc *Document, err error) {
	defer func() { // 该库遇到异常 PDF 可能 panic，必须兜底
		if rec := recover(); rec != nil {
			doc, err = nil, fmt.Errorf("panic: %v", rec)
		}
	}()
	rd, err := pdf.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	doc = &Document{Parser: p.Name()}
	for i := 1; i <= rd.NumPage(); i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		pg := rd.Page(i)
		if pg.V.IsNull() {
			continue
		}
		fonts := map[string]*pdf.Font{}
		for _, n := range pg.Fonts() {
			f := pg.Font(n)
			fonts[n] = &f
		}
		txt, err := pg.GetPlainText(fonts)
		if err != nil {
			return nil, err
		}
		doc.Blocks = append(doc.Blocks, Block{Kind: KindPageText, Text: txt, Page: i})
	}
	return doc, nil
}

// PdfiumPDF 通过 go-pdfium（MIT）的 WebAssembly 模式运行 PDFium（Apache-2.0/BSD），无 cgo。
// 首次解析 PDF 时才初始化（冷启动约 2–3 s），之后复用单实例；实例非并发安全，用互斥锁串行。
type PdfiumPDF struct {
	once sync.Once
	mu   sync.Mutex
	inst pdfium.Pdfium
	err  error
}

func (p *PdfiumPDF) init() {
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
	if err != nil {
		p.err = err
		return
	}
	p.inst, p.err = pool.GetInstance(60 * time.Second)
}

func (*PdfiumPDF) Name() string           { return "pdf-pdfium-wasm" }
func (*PdfiumPDF) Supports(e string) bool { return e == "pdf" }
func (p *PdfiumPDF) Parse(ctx context.Context, r io.ReaderAt, size int64) (*Document, error) {
	p.once.Do(p.init)
	if p.err != nil {
		return nil, p.err
	}
	b, err := io.ReadAll(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	od, err := p.inst.OpenDocument(&requests.OpenDocument{File: &b})
	if err != nil {
		return nil, err
	}
	defer func() { _, _ = p.inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: od.Document}) }()
	pc, err := p.inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: od.Document})
	if err != nil {
		return nil, err
	}
	doc := &Document{Parser: p.Name()}
	for i := 0; i < pc.PageCount; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		t, err := p.inst.GetPageText(&requests.GetPageText{Page: requests.Page{ByIndex: &requests.PageByIndex{Document: od.Document, Index: i}}})
		if err != nil {
			return nil, err
		}
		doc.Blocks = append(doc.Blocks, Block{Kind: KindPageText, Text: t.Text, Page: i + 1})
	}
	return doc, nil
}
