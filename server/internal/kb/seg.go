// Package kb 知识库（M1 第四块，FR-16/17/18）：切片、embedding、chunk 存储与检索。
package kb

import (
	"strings"
	"sync"
	"unicode"

	"github.com/go-ego/gse"
)

var (
	segOnce sync.Once
	seg     gse.Segmenter
	segErr  error
)

// Terms 中文全文检索的分词（gse 搜索模式，含长词的子词），只保留含字母/数字/汉字的词，统一小写。
// 首次调用加载内置词典（约 0.4s）。
func Terms(text string) []string {
	segOnce.Do(func() {
		seg.SkipLog = true
		segErr = seg.LoadDictEmbed("zh_s")
	})
	var raw []string
	if segErr != nil {
		raw = strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	} else {
		raw = seg.CutSearch(text, true)
	}
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || !strings.ContainsFunc(t, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			continue
		}
		// tsvector 词条只保留字母、数字、汉字，避免 to_tsvector/to_tsquery 的特殊字符问题
		t = strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return r
			}
			return -1
		}, t)
		if t != "" && len(t) <= 200 {
			out = append(out, t)
		}
	}
	return out
}

// TSText 写入 kb_chunks.tsv 的文本：分好的词以空格连接，由 to_tsvector('simple', ...) 建索引。
func TSText(text string) string { return strings.Join(Terms(text), " ") }

// TSQuery 检索用 tsquery（词之间 OR，排名交给 ts_rank）；没有可用词时返回空串。
func TSQuery(q string) string {
	seen := map[string]bool{}
	var parts []string
	for _, t := range Terms(q) {
		if !seen[t] {
			seen[t] = true
			parts = append(parts, "'"+t+"'")
		}
	}
	return strings.Join(parts, " | ")
}
