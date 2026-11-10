package aigc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/aigc"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

func TestDefaultsValidateSave(t *testing.T) {
	if err := aigc.Defaults().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (aigc.Settings{Badge: "生成", Suffix: aigc.DefaultSuffix}).Validate(); err == nil {
		t.Fatal("缺「人工智能生成/AI 生成」应失败")
	}
	if err := (aigc.Settings{Badge: "AI 生成", Suffix: ""}).Validate(); err == nil {
		t.Fatal("空后缀应失败")
	}
	if err := (aigc.Settings{Badge: "", Suffix: aigc.DefaultSuffix}).Validate(); err == nil {
		t.Fatal("空角标应失败")
	}
	if err := (aigc.Settings{Badge: "AI 合成", Suffix: aigc.DefaultSuffix}).Validate(); err == nil {
		t.Fatal("「AI 合成」不含规定短语应失败")
	}
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	st, err := aigc.Load(ctx, c)
	if err != nil || st.Badge != aigc.DefaultBadge {
		t.Fatalf("%+v %v", st, err)
	}
	if err := aigc.Save(ctx, c, aigc.Settings{Badge: "", Suffix: aigc.DefaultSuffix}, 1); err == nil {
		t.Fatal("空文案保存应失败")
	}
	st.Badge = "人工智能生成"
	st.Suffix = "由 AI 生成"
	if err := aigc.Save(ctx, c, st, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := aigc.Load(ctx, c)
	if got.Badge != "人工智能生成" || got.Suffix != "由 AI 生成" {
		t.Fatalf("%+v", got)
	}
}

func TestAppendSuffixAndMeta(t *testing.T) {
	s := aigc.AppendSuffix("你好", aigc.DefaultSuffix)
	if !strings.Contains(s, aigc.DefaultSuffix) || !strings.HasPrefix(s, "你好") {
		t.Fatalf("%q", s)
	}
	if aigc.AppendSuffix(s, aigc.DefaultSuffix) != s {
		t.Fatal("重复附加")
	}
	m := aigc.BuildMeta(aigc.Defaults(), "req-1", "")
	h := aigc.HeaderValue(m)
	if !strings.Contains(h, `"AIGC"`) || !strings.Contains(h, "req-1") || !strings.Contains(h, aigc.DefaultProducer) {
		t.Fatalf("%s", h)
	}
	fm := aigc.MarkdownFrontMatter(m)
	if !strings.HasPrefix(fm, "---\n") || !strings.Contains(fm, "AIGC:") || !strings.Contains(fm, "req-1") || !strings.Contains(fm, "ContentProducer:") {
		t.Fatalf("front matter: %s", fm)
	}
}
