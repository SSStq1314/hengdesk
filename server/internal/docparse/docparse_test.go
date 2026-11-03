package docparse

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// minimalPDF 生成一页文本 PDF（Helvetica，ASCII），xref 偏移正确。
func minimalPDF(text string) []byte {
	var b bytes.Buffer
	var offs []int
	obj := func(s string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), s)
	}
	b.WriteString("%PDF-1.4\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>")
	stream := fmt.Sprintf("BT /F1 18 Tf 72 700 Td (%s) Tj ET", text)
	obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, x)
	return b.Bytes()
}

func read(t *testing.T, n string) []byte {
	b, err := os.ReadFile("testdata/" + n)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDetectAndExtract(t *testing.T) {
	x := New()
	ctx := context.Background()
	for _, c := range []struct {
		name string
		data []byte
		kind string
		want string
	}{
		{"合同.docx", read(t, "zh_report.docx"), "docx", "季度"},
		{"s.pptx", read(t, "zh_slides.pptx"), "pptx", ""},
		{"t.xlsx", read(t, "zh_table.xlsx"), "xlsx", " | "},
		{"r.md", read(t, "zh_report.md"), "md", ""},
		{"a.txt", []byte("hello 世界\n第二行"), "txt", "世界"},
		{"a.pdf", minimalPDF("Contract risk clause 42"), "pdf", "risk clause 42"},
	} {
		k, err := Detect(c.name, c.data)
		if err != nil || k != c.kind {
			t.Fatalf("%s detect %q %v", c.name, k, err)
		}
		txt, err := x.Extract(ctx, k, c.data)
		if err != nil || !strings.Contains(txt, c.want) {
			t.Fatalf("%s extract %v %q", c.name, err, txt)
		}
	}
}

func TestDetectRejects(t *testing.T) {
	docx := read(t, "zh_report.docx")
	var macro bytes.Buffer
	zw := zip.NewWriter(&macro)
	for _, n := range []string{"[Content_Types].xml", "word/document.xml", "word/vbaProject.bin"} {
		w, _ := zw.Create(n)
		_, _ = w.Write([]byte("x"))
	}
	_ = zw.Close()
	for _, c := range []struct {
		name string
		data []byte
		err  error
	}{
		{"a.exe", []byte("MZ"), ErrUnsupported},
		{"a.doc", []byte("x"), ErrUnsupported},
		{"a.docm", docx, ErrUnsupported},
		{"伪装.pdf", docx, ErrMismatch},            // 扩展名是 pdf，内容是 docx
		{"伪装.xlsx", docx, ErrMismatch},           // 内容是 docx
		{"伪装.txt", minimalPDF("x"), ErrMismatch}, // 内容是 PDF
		{"bin.txt", []byte{0x00, 0x01, 0x02, 'a'}, ErrMismatch},
		{"gbk.txt", []byte{0xc4, 0xe3, 0xba, 0xc3}, ErrMismatch},
		{"zip.docx", []byte("PK\x03\x04garbage"), ErrMismatch},
		{"m.docx", macro.Bytes(), ErrMacro},
	} {
		if _, err := Detect(c.name, c.data); !errors.Is(err, c.err) {
			t.Fatalf("%s: want %v got %v", c.name, c.err, err)
		}
	}
}

func TestExtractBrokenAndEmpty(t *testing.T) {
	x := New()
	if _, err := x.Extract(context.Background(), "docx", read(t, "zh_trunc.docx")); err == nil {
		t.Fatal("截断的 docx 应解析失败")
	}
	if _, err := x.Extract(context.Background(), "pdf", []byte("%PDF-1.4\ngarbage")); err == nil {
		t.Fatal("坏 PDF 应解析失败")
	}
	if _, err := x.Extract(context.Background(), "txt", []byte("  \n ")); !errors.Is(err, ErrEmpty) {
		t.Fatalf("空文本应 ErrEmpty: %v", err)
	}
}
