package kb

import (
	"strings"
	"unicode/utf8"

	"github.com/SSStq1314/hengdesk/server/internal/docparse"
)

// 切片参数默认值与范围（docs/progress/m1-block4.md 待确认 3）。
const (
	DefaultChunkSize    = 800
	DefaultChunkOverlap = 100
	MinChunkSize        = 200
	MaxChunkSize        = 4000
	// MaxDocRunes 单文件抽取文本上限（约 400 万字），超出按「文件过大」失败，避免一次索引占满 embedding 配额。
	MaxDocRunes = 4_000_000
)

// ValidChunking 校验切片参数。
func ValidChunking(size, overlap int) bool {
	return size >= MinChunkSize && size <= MaxChunkSize && overlap >= 0 && overlap*2 < size
}

// Piece 一个切片。Start/End 是在整篇抽取文本（Document.PlainText 规则）中的字符（rune）偏移。
type Piece struct {
	Seq     int
	Content string
	Heading string
	Start   int
	End     int
}

type unit struct {
	text    string
	heading string
	start   int
	table   bool
}

// Split 按结构切片：标题作为后续段落的上下文（不单独成片），段落累积到 size 字符；
// 超长段落/表格按 size 窗口、overlap 重叠切开（表格优先按行边界）。相邻切片在段落边界处带 overlap 字符的上文。
func Split(d *docparse.Document, size, overlap int) []Piece {
	if !ValidChunking(size, overlap) {
		size, overlap = DefaultChunkSize, DefaultChunkOverlap
	}
	var units []unit
	pos := 0
	heading := ""
	for _, bl := range d.Blocks {
		var text string
		if bl.Kind == docparse.KindTable {
			rows := make([]string, 0, len(bl.Rows))
			for _, r := range bl.Rows {
				rows = append(rows, strings.Join(r, " | "))
			}
			text = strings.Join(rows, "\n")
		} else {
			text = bl.Text
		}
		n := utf8.RuneCountInString(text) + 1 // PlainText 每块后面加一个换行
		t := strings.TrimSpace(strings.ToValidUTF8(text, ""))
		if bl.Kind == docparse.KindHeading {
			if t != "" {
				heading = truncRunes(t, 200)
			}
		} else if t != "" {
			units = append(units, unit{text: t, heading: heading, start: pos, table: bl.Kind == docparse.KindTable})
		}
		pos += n
	}
	var out []Piece
	emit := func(content, h string, start, end int) {
		if strings.TrimSpace(content) == "" {
			return
		}
		out = append(out, Piece{Seq: len(out), Content: content, Heading: h, Start: start, End: end})
	}
	var buf strings.Builder
	bufLen, bufStart, bufEnd, bufHead := 0, 0, 0, ""
	flush := func() {
		if bufLen > 0 {
			emit(buf.String(), bufHead, bufStart, bufEnd)
		}
		buf.Reset()
		bufLen = 0
	}
	for _, u := range units {
		ul := utf8.RuneCountInString(u.text)
		if ul > size {
			flush()
			for _, w := range window(u.text, size, overlap, u.table) {
				emit(w.text, u.heading, u.start+w.start, u.start+w.start+utf8.RuneCountInString(w.text))
			}
			continue
		}
		if bufLen > 0 && (bufLen+1+ul > size || u.heading != bufHead) {
			prev := buf.String()
			flush()
			// 同一标题下延续时带上一片末尾 overlap 字符作为上文
			if u.heading == bufHead && overlap > 0 {
				tail := lastRunes(prev, overlap)
				if utf8.RuneCountInString(tail)+1+ul <= size {
					buf.WriteString(tail)
					bufLen = utf8.RuneCountInString(tail)
					bufStart = bufEnd - bufLen
				}
			}
		}
		if bufLen == 0 {
			bufStart = u.start
			bufHead = u.heading
		} else {
			buf.WriteByte('\n')
			bufLen++
		}
		buf.WriteString(u.text)
		bufLen += ul
		bufEnd = u.start + ul
	}
	flush()
	return out
}

type win struct {
	text  string
	start int
}

// window 把超长文本切成不超过 size 字符的窗口，步长 size-overlap；表格尽量在行边界处断开。
func window(s string, size, overlap int, lines bool) []win {
	rs := []rune(s)
	var out []win
	for i := 0; i < len(rs); {
		end := min(i+size, len(rs))
		if end < len(rs) {
			// 优先在换行/句末标点处断开（不早于窗口的一半）
			for j := end; j > i+size/2; j-- {
				c := rs[j-1]
				if c == '\n' || (!lines && strings.ContainsRune("。！？；.!?;", c)) {
					end = j
					break
				}
			}
		}
		out = append(out, win{text: strings.TrimSpace(string(rs[i:end])), start: i})
		if end >= len(rs) {
			break
		}
		next := end - overlap
		if next <= i {
			next = end
		}
		i = next
	}
	return out
}

func lastRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[len(rs)-n:])
}

func truncRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}
