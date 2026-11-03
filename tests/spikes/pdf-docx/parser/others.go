package parser

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/ledongthuc/pdf"
	"github.com/xuri/excelize/v2"
)

// XlsxParser 使用 excelize（BSD-3-Clause），每个工作表一个表格块。
type XlsxParser struct{}

func (XlsxParser) Name() string           { return "xlsx-excelize" }
func (XlsxParser) Supports(e string) bool { return e == "xlsx" }
func (XlsxParser) Parse(ctx context.Context, r io.ReaderAt, size int64) (*Document, error) {
	f, err := excelize.OpenReader(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	doc := &Document{Parser: "xlsx-excelize"}
	for i, sh := range f.GetSheetList() {
		rows, err := f.GetRows(sh)
		if err != nil {
			return nil, err
		}
		doc.Blocks = append(doc.Blocks, Block{Kind: KindHeading, Text: sh, Page: i + 1})
		doc.Blocks = append(doc.Blocks, Block{Kind: KindTable, Rows: rows, Page: i + 1})
	}
	return doc, nil
}

// TextParser 处理 md/txt（md 原样保留，Markdown 结构交给切片器）。
type TextParser struct{}

func (TextParser) Name() string           { return "text" }
func (TextParser) Supports(e string) bool { return e == "md" || e == "txt" || e == "markdown" }
func (TextParser) Parse(ctx context.Context, r io.ReaderAt, size int64) (*Document, error) {
	b, err := io.ReadAll(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, err
	}
	doc := &Document{Parser: "text"}
	for _, p := range strings.Split(string(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))), "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			doc.Blocks = append(doc.Blocks, Block{Kind: KindParagraph, Text: p})
		}
	}
	return doc, nil
}

// LedongthucPDF 纯 Go（BSD-3-Clause）。Mode "plain" 用 GetPlainText，"rows" 用 GetTextByRow（按 Y 聚行）。
type LedongthucPDF struct{ Mode string }

func (p LedongthucPDF) Name() string         { return "pdf-ledongthuc-" + p.Mode }
func (LedongthucPDF) Supports(e string) bool { return e == "pdf" }
func (p LedongthucPDF) Parse(ctx context.Context, r io.ReaderAt, size int64) (doc *Document, err error) {
	defer func() { // 该库遇到异常 PDF 可能 panic，必须兜底
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic: %v", rec)
		}
	}()
	rd, err := pdf.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	doc = &Document{Parser: p.Name()}
	for i := 1; i <= rd.NumPage(); i++ {
		pg := rd.Page(i)
		if pg.V.IsNull() {
			continue
		}
		var txt string
		if p.Mode == "rows" {
			rows, err := pg.GetTextByRow()
			if err != nil {
				return nil, err
			}
			var b strings.Builder
			for _, row := range rows {
				for j, w := range row.Content {
					if j > 0 {
						b.WriteByte(' ')
					}
					b.WriteString(w.S)
				}
				b.WriteByte('\n')
			}
			txt = b.String()
		} else {
			fonts := map[string]*pdf.Font{}
			for _, n := range pg.Fonts() {
				f := pg.Font(n)
				fonts[n] = &f
			}
			txt, err = pg.GetPlainText(fonts)
			if err != nil {
				return nil, err
			}
		}
		doc.Blocks = append(doc.Blocks, Block{Kind: KindPageText, Text: txt, Page: i})
	}
	return doc, nil
}

// PdfiumPDF 通过 go-pdfium（MIT）的 WebAssembly 模式运行 PDFium（Apache-2.0/BSD），无 cgo。
type PdfiumPDF struct{ Inst pdfium.Pdfium }

func NewPdfium() (*PdfiumPDF, func(), error) {
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1})
	if err != nil {
		return nil, nil, err
	}
	inst, err := pool.GetInstance(60e9)
	if err != nil {
		return nil, nil, err
	}
	return &PdfiumPDF{Inst: inst}, func() { inst.Close(); pool.Close() }, nil
}

func (PdfiumPDF) Name() string           { return "pdf-pdfium-wasm" }
func (PdfiumPDF) Supports(e string) bool { return e == "pdf" }
func (p PdfiumPDF) Parse(ctx context.Context, r io.ReaderAt, size int64) (*Document, error) {
	b, err := io.ReadAll(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, err
	}
	od, err := p.Inst.OpenDocument(&requests.OpenDocument{File: &b})
	if err != nil {
		return nil, err
	}
	defer p.Inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: od.Document})
	pc, err := p.Inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: od.Document})
	if err != nil {
		return nil, err
	}
	doc := &Document{Parser: p.Name()}
	for i := 0; i < pc.PageCount; i++ {
		t, err := p.Inst.GetPageText(&requests.GetPageText{Page: requests.Page{ByIndex: &requests.PageByIndex{Document: od.Document, Index: i}}})
		if err != nil {
			return nil, err
		}
		doc.Blocks = append(doc.Blocks, Block{Kind: KindPageText, Text: t.Text, Page: i + 1})
	}
	return doc, nil
}

var _ = os.Open
