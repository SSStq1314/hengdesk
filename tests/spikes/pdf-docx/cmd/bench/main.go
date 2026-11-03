// 评测：对样本跑每个解析器，统计字符召回、多栏阅读顺序、表格行保留、耗时。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"hengdesk-spikes/pdfdocx/parser"
)

type truth struct {
	Title string     `json:"title"`
	Paras []string   `json:"paras"`
	Cols  []string   `json:"cols"`
	Table [][]string `json:"table"`
}

func norm(s string) []rune {
	var out []rune
	for _, r := range s {
		if !unicode.IsSpace(r) && r != '|' {
			out = append(out, r)
		}
	}
	return out
}

func lcs(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
			} else if cur[j-1] > prev[j] {
				cur[j] = cur[j-1]
			} else {
				cur[j] = prev[j]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// rowKept: 某表格行的所有单元格按顺序出现在同一输出行（或结构化表格行）中。
func rowKept(doc *parser.Document, row []string) bool {
	check := func(line string) bool {
		l := string(norm(line))
		pos := 0
		for _, c := range row {
			i := strings.Index(l[pos:], string(norm(c)))
			if i < 0 {
				return false
			}
			pos += i + len(string(norm(c)))
		}
		return true
	}
	for _, line := range strings.Split(doc.PlainText(), "\n") {
		if check(line) {
			return true
		}
	}
	return false
}

type result struct {
	File, Parser    string
	OK              bool
	Err             string `json:",omitempty"`
	CharRecall      float64
	CharPrecision   float64
	WordRecall      float64 `json:",omitempty"` // 仅英文：真值单词作为完整 token 出现的比例（检测词被空格拆碎）
	ColumnsInOrder  bool
	TableRowsKept   string
	StructuredTable bool
	Millis          float64
	Pages           int
	MsPerPage       float64 `json:",omitempty"`
	Sample          string
}

func main() {
	dir := flag.String("dir", "samples", "")
	out := flag.String("out", "results.json", "")
	reps := flag.Int("reps", 5, "")
	flag.Parse()
	ctx := context.Background()
	pdfium, closeP, err := parser.NewPdfium()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pdfium init:", err)
		os.Exit(1)
	}
	defer closeP()
	all := []parser.Parser{parser.LedongthucPDF{Mode: "plain"}, parser.LedongthucPDF{Mode: "rows"}, pdfium,
		parser.DocxParser{}, parser.PptxParser{}, parser.XlsxParser{}, parser.TextParser{}}
	files, _ := filepath.Glob(filepath.Join(*dir, "*"))
	var res []result
	for _, f := range files {
		ext := strings.TrimPrefix(filepath.Ext(f), ".")
		base := filepath.Base(f)
		lang := base[:2]
		var tr truth
		tb, _ := os.ReadFile(filepath.Join(*dir, lang+"_truth.json"))
		json.Unmarshal(tb, &tr)
		for _, p := range all {
			if !p.Supports(ext) {
				continue
			}
			fh, _ := os.Open(f)
			st, _ := fh.Stat()
			r := result{File: base, Parser: p.Name()}
			var doc *parser.Document
			t0 := time.Now()
			for i := 0; i < *reps; i++ {
				doc, err = p.Parse(ctx, fh, st.Size())
				if err != nil {
					break
				}
			}
			r.Millis = float64(time.Since(t0).Microseconds()) / 1000 / float64(*reps)
			fh.Close()
			if err != nil {
				r.Err = err.Error()
				res = append(res, r)
				continue
			}
			r.OK = true
			txt := doc.PlainText()
			for _, b := range doc.Blocks {
				if b.Kind == parser.KindTable {
					r.StructuredTable = true
				}
				if b.Page > r.Pages {
					r.Pages = b.Page
				}
			}
			if r.Pages > 0 {
				r.MsPerPage = r.Millis / float64(r.Pages)
			}
			if strings.Contains(base, "big") {
				r.Sample = fmt.Sprintf("%d chars", len([]rune(txt)))
				res = append(res, r)
				continue
			}
			// 真值：按文件类型取应出现的内容
			var want []string
			switch {
			case strings.Contains(base, "_table"):
				for _, row := range tr.Table {
					want = append(want, strings.Join(row, ""))
				}
			case strings.Contains(base, "_slides"):
				want = []string{tr.Title, tr.Paras[0], tr.Paras[1], tr.Paras[2]}
			default:
				want = append([]string{tr.Title}, tr.Paras...)
				for _, row := range tr.Table {
					want = append(want, strings.Join(row, " "))
				}
				want = append(want, tr.Cols...)
			}
			W, O := norm(strings.Join(want, "")), norm(txt)
			l := lcs(W, O)
			r.CharRecall = float64(l) / float64(len(W))
			if len(O) > 0 {
				r.CharPrecision = float64(l) / float64(len(O))
			}
			if lang == "en" {
				toks := map[string]int{}
				for _, w := range strings.FieldsFunc(txt, func(r rune) bool { return unicode.IsSpace(r) || r == '|' }) {
					toks[strings.Trim(w, ".,:;")]++
				}
				tw, hit := 0, 0
				for _, w := range strings.Fields(strings.Join(want, " ")) {
					tw++
					if toks[strings.Trim(w, ".,:;")] > 0 {
						hit++
					}
				}
				r.WordRecall = float64(hit) / float64(tw)
			}
			if !strings.Contains(base, "_table") && !strings.Contains(base, "_slides") {
				on := string(O)
				r.ColumnsInOrder = strings.Contains(on, string(norm(tr.Cols[0]))) && strings.Contains(on, string(norm(tr.Cols[1])))
			}
			if !strings.Contains(base, "_slides") {
				k := 0
				for _, row := range tr.Table {
					if rowKept(doc, row) {
						k++
					}
				}
				r.TableRowsKept = fmt.Sprintf("%d/%d", k, len(tr.Table))
			}
			s := []rune(strings.Join(strings.Fields(txt), " "))
			if len(s) > 160 {
				s = s[:160]
			}
			r.Sample = string(s)
			res = append(res, r)
		}
	}
	fmt.Printf("%-18s %-22s %-5s %-7s %-7s %-6s %-6s %-6s %9s %8s\n", "file", "parser", "ok", "recall", "prec", "cols", "table", "struct", "ms", "ms/page")
	fmt.Println("(wordRecall 见 results.json)")
	for _, r := range res {
		fmt.Printf("%-18s %-22s %-5v %-7.3f %-7.3f %-6v %-6s %-6v %9.2f %8.2f %s\n", r.File, r.Parser, r.OK, r.CharRecall, r.CharPrecision, r.ColumnsInOrder, r.TableRowsKept, r.StructuredTable, r.Millis, r.MsPerPage, r.Err)
		if r.WordRecall > 0 {
			fmt.Printf("%40s wordRecall=%.3f\n", "", r.WordRecall)
		}
	}
	b, _ := json.MarshalIndent(res, "", " ")
	os.WriteFile(*out, b, 0o644)
}
