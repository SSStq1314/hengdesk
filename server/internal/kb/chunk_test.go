package kb

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SSStq1314/hengdesk/server/internal/docparse"
)

func TestValidChunking(t *testing.T) {
	for _, c := range []struct {
		s, o int
		ok   bool
	}{{800, 100, true}, {200, 0, true}, {199, 0, false}, {4001, 0, false}, {400, 200, false}, {400, -1, false}} {
		if ValidChunking(c.s, c.o) != c.ok {
			t.Errorf("%d/%d", c.s, c.o)
		}
	}
}

func TestSplitStructure(t *testing.T) {
	d := &docparse.Document{Blocks: []docparse.Block{
		{Kind: docparse.KindHeading, Text: "差旅制度"},
		{Kind: docparse.KindParagraph, Text: "住宿每晚不超过 500 元。"},
		{Kind: docparse.KindParagraph, Text: strings.Repeat("长段落内容。", 200)},
		{Kind: docparse.KindHeading, Text: "附表"},
		{Kind: docparse.KindTable, Rows: [][]string{{"城市", "标准"}, {"北京", "600"}}},
	}}
	ps := Split(d, 300, 50)
	if len(ps) < 4 {
		t.Fatalf("pieces %d", len(ps))
	}
	if ps[0].Heading != "差旅制度" || !strings.Contains(ps[0].Content, "500") {
		t.Fatalf("first %+v", ps[0])
	}
	last := ps[len(ps)-1]
	if last.Heading != "附表" || !strings.Contains(last.Content, "北京 | 600") {
		t.Fatalf("table %+v", last)
	}
	for i, p := range ps {
		if p.Seq != i || utf8.RuneCountInString(p.Content) > 300+50 || p.End < p.Start {
			t.Fatalf("piece %d: seq=%d len=%d %d-%d", i, p.Seq, utf8.RuneCountInString(p.Content), p.Start, p.End)
		}
	}
	// 非法参数退回默认值
	if n := len(Split(d, 10, 50)); n == 0 {
		t.Fatal("fallback")
	}
	if len(Split(&docparse.Document{Blocks: []docparse.Block{{Kind: docparse.KindParagraph, Text: "  "}}}, 800, 100)) != 0 {
		t.Fatal("blank produced piece")
	}
}

func TestTerms(t *testing.T) {
	got := Terms("员工出差住宿标准 Hello, World! 2026")
	j := " " + strings.Join(got, " ") + " "
	for _, w := range []string{" 出差 ", " 住宿 ", " hello ", " world ", " 2026 "} {
		if !strings.Contains(j, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	if strings.ContainsAny(j, ",!") {
		t.Errorf("punct %v", got)
	}
	if q := TSQuery("出差 住宿"); !strings.Contains(q, "|") || strings.Contains(q, "&") {
		t.Errorf("tsquery %q", q)
	}
	if TSQuery("，。！") != "" {
		t.Error("empty query")
	}
}
