package parser

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// DocxParser 基于标准库 archive/zip + encoding/xml 自研，零第三方依赖。
// 保留：标题（pStyle Heading*/标题*）、段落、表格（含嵌套单元格文本）。
type DocxParser struct{}

func (DocxParser) Name() string           { return "docx-stdlib" }
func (DocxParser) Supports(e string) bool { return e == "docx" }

func readZipFile(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 200<<20)) // 防 zip bomb
		}
	}
	return nil, fmt.Errorf("%s not found", name)
}

func (DocxParser) Parse(ctx context.Context, r io.ReaderAt, size int64) (*Document, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	data, err := readZipFile(zr, "word/document.xml")
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	doc := &Document{Parser: "docx-stdlib"}
	var (
		para     strings.Builder
		style    string
		inText   bool
		tblDepth int
		rows     [][]string
		row      []string
		cell     strings.Builder
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tbl":
				tblDepth++
				if tblDepth == 1 {
					rows = nil
				}
			case "tr":
				if tblDepth == 1 {
					row = nil
				}
			case "tc":
				if tblDepth == 1 {
					cell.Reset()
				}
			case "p":
				para.Reset()
				style = ""
			case "pStyle":
				for _, a := range t.Attr {
					if a.Name.Local == "val" {
						style = a.Value
					}
				}
			case "t":
				inText = true
			case "tab":
				para.WriteByte('\t')
			case "br":
				para.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				txt := strings.TrimSpace(para.String())
				if tblDepth > 0 {
					if cell.Len() > 0 && txt != "" {
						cell.WriteByte('\n')
					}
					cell.WriteString(txt)
				} else if txt != "" {
					bl := Block{Kind: KindParagraph, Text: txt}
					ls := strings.ToLower(style)
					if strings.HasPrefix(ls, "heading") || strings.HasPrefix(ls, "title") || strings.Contains(style, "标题") {
						bl.Kind = KindHeading
						bl.Level, _ = strconv.Atoi(strings.TrimLeft(ls, "headingtitle"))
					}
					doc.Blocks = append(doc.Blocks, bl)
				}
			case "tc":
				if tblDepth == 1 {
					row = append(row, cell.String())
				}
			case "tr":
				if tblDepth == 1 {
					rows = append(rows, row)
				}
			case "tbl":
				if tblDepth == 1 {
					doc.Blocks = append(doc.Blocks, Block{Kind: KindTable, Rows: rows})
				}
				tblDepth--
			}
		case xml.CharData:
			if inText {
				para.Write(t)
			}
		}
	}
	return doc, nil
}

// PptxParser 按幻灯片顺序读取 ppt/slides/slideN.xml 中的 a:t / a:p。
type PptxParser struct{}

func (PptxParser) Name() string           { return "pptx-stdlib" }
func (PptxParser) Supports(e string) bool { return e == "pptx" }

func (PptxParser) Parse(ctx context.Context, r io.ReaderAt, size int64) (*Document, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	type sl struct {
		n    int
		name string
	}
	var slides []sl
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(f.Name, "ppt/slides/slide"), ".xml"))
			slides = append(slides, sl{n, f.Name})
		}
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].n < slides[j].n })
	doc := &Document{Parser: "pptx-stdlib"}
	for _, s := range slides {
		data, err := readZipFile(zr, s.name)
		if err != nil {
			return nil, err
		}
		dec := xml.NewDecoder(strings.NewReader(string(data)))
		var para strings.Builder
		inT := false
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "t" {
					inT = true
				} else if t.Name.Local == "p" {
					para.Reset()
				}
			case xml.EndElement:
				if t.Name.Local == "t" {
					inT = false
				} else if t.Name.Local == "p" {
					if txt := strings.TrimSpace(para.String()); txt != "" {
						doc.Blocks = append(doc.Blocks, Block{Kind: KindParagraph, Text: txt, Page: s.n})
					}
				}
			case xml.CharData:
				if inT {
					para.Write(t)
				}
			}
		}
	}
	return doc, nil
}
