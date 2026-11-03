package docparse

import (
	"archive/zip"
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// 允许的类型（FR-15）：txt / md / docx / xlsx / pptx / 文本 PDF。其它一律拒绝（含 .doc/.xls/.ppt 老格式、
// 带宏的 docm/xlsm/pptm、图片、压缩包）。
var allowedExt = map[string]string{"txt": "txt", "md": "md", "markdown": "md", "docx": "docx", "xlsx": "xlsx", "pptx": "pptx", "pdf": "pdf"}

const (
	maxEntryBytes  = 64 << 20  // 单个 zip 条目解压上限
	maxTotalUnzip  = 256 << 20 // zip 声明的解压总量上限
	maxZipEntries  = 10000
	maxDocTextRune = 2_000_000 // 抽取结果上限（存库前再按上下文阈值截断）
)

var (
	// ErrUnsupported 扩展名不在白名单。
	ErrUnsupported = errors.New("unsupported")
	// ErrMismatch 文件内容与扩展名不符（不信扩展名）。
	ErrMismatch = errors.New("mismatch")
	// ErrMacro 含宏（vbaProject.bin）。
	ErrMacro = errors.New("macro")
	// ErrBomb 压缩包条目过多或解压后过大。
	ErrBomb = errors.New("bomb")
)

// Detect 按文件内容判定类型，并要求与扩展名一致。返回规范类型（txt/md/docx/xlsx/pptx/pdf）。
func Detect(name string, data []byte) (string, error) {
	want, ok := allowedExt[strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))]
	if !ok {
		return "", ErrUnsupported
	}
	got, err := sniff(data)
	if err != nil {
		return "", err
	}
	if got == "text" && (want == "txt" || want == "md") {
		return want, nil
	}
	if got != want {
		return "", ErrMismatch
	}
	return got, nil
}

func sniff(data []byte) (string, error) {
	switch {
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return "pdf", nil
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return sniffOOXML(data)
	}
	// 纯文本：合法 UTF-8 且不含 NUL 等二进制控制字符
	t := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(t) {
		return "", ErrMismatch
	}
	for _, b := range t {
		if b < 0x09 || (b > 0x0d && b < 0x20 && b != 0x1b) {
			return "", ErrMismatch
		}
	}
	return "text", nil
}

func sniffOOXML(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", ErrMismatch
	}
	if len(zr.File) > maxZipEntries {
		return "", ErrBomb
	}
	var total uint64
	has := map[string]bool{}
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if f.UncompressedSize64 > maxEntryBytes || total > maxTotalUnzip {
			return "", ErrBomb
		}
		n := strings.ToLower(f.Name)
		if strings.HasSuffix(n, "vbaproject.bin") {
			return "", ErrMacro
		}
		has[n] = true
	}
	if !has["[content_types].xml"] {
		return "", ErrMismatch
	}
	switch {
	case has["word/document.xml"]:
		return "docx", nil
	case has["xl/workbook.xml"]:
		return "xlsx", nil
	case has["ppt/presentation.xml"]:
		return "pptx", nil
	}
	return "", ErrMismatch
}
