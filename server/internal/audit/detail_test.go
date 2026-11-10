package audit_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

// usageLike 字段声明顺序与字母序不同（复现 D5：gateway.Usage 直接放进 detail）。
type usageLike struct {
	Prompt     int     `json:"prompt_tokens"`
	Completion int     `json:"completion_tokens"`
	Total      int     `json:"total_tokens"`
	Zeta       string  `json:"zeta"`
	Alpha      float64 `json:"alpha"`
	Inner      *inner  `json:"inner,omitempty"`
	Skip       int     `json:"skip,omitempty"`
}

type inner struct {
	Z []any          `json:"z"`
	A map[string]int `json:"a"`
}

type label string

// D5Details 各种容易让入链哈希与 jsonb 往返后重算哈希不一致的 detail。
func D5Details() []map[string]any {
	return []map[string]any{
		{"usage": usageLike{Prompt: 12, Completion: 34, Total: 46, Zeta: "z", Alpha: 0.5}},
		{"usage": &usageLike{Prompt: 1, Inner: &inner{Z: []any{3, "b", nil, true, 1.25}, A: map[string]int{"y": 2, "b": 1}}}},
		{"n_int": 7, "n_int64": int64(1) << 40, "n_big": int64(9007199254740993), "n_neg": -3, "n_float": 3.14159, "n_exp": 1e21, "n_small": 1e-7, "u8": uint8(255)},
		{"中文": "审计链 校验", "emoji": "🙂", "quote": `he said "hi" \ back\slash`, "html": "<a href='x'>&amp;</a>", "ctrl": "tab\tnl\ncr\r", "uni": "\u2028\u2029"},
		{"nested": map[string]any{"b": []map[string]any{{"y": 1, "x": label("名")}}, "a": []int{3, 2, 1}}, "empty_m": map[string]any{}, "empty_s": []string{}, "nil": nil},
		{"typed": label("x"), "bool": false, "slice_struct": []usageLike{{Total: 1}, {Total: 2, Zeta: "末"}}},
		{},
		nil,
	}
}

func writeD5(t *testing.T, c *ent.Client) int {
	t.Helper()
	ctx := context.Background()
	w := audit.EntWriter{C: c}
	n := 0
	for i, d := range D5Details() {
		if err := w.Write(ctx, audit.Entry{Action: "model.call", Phase: "result", ActorID: i + 1, Detail: d}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		n++
	}
	return n
}

// TestChainVerifyStructAndSpecialDetails D5 回归（SQLite 路径）。
func TestChainVerifyStructAndSpecialDetails(t *testing.T) {
	c := storetest.NewTestClient(t)
	n := writeD5(t, c)
	res, err := audit.Verify(context.Background(), c)
	if err != nil || !res.OK || res.Checked != n {
		t.Fatalf("verify %+v %v", res, err)
	}
}

func TestNormalizeDetailRejectsUnserializable(t *testing.T) {
	if _, err := audit.NormalizeDetail(map[string]any{"ch": make(chan int)}); err == nil {
		t.Fatal("want error for unserializable detail (fail-closed)")
	}
	c := storetest.NewTestClient(t)
	if err := (audit.EntWriter{C: c}).Write(context.Background(), audit.Entry{Action: "x", Detail: map[string]any{"f": func() {}}}); err == nil {
		t.Fatal("write must fail closed")
	}
}

func TestNormalizeDetailMatchesJSONRoundTrip(t *testing.T) {
	raw := map[string]any{"usage": usageLike{Prompt: 12, Completion: 34, Total: 46, Zeta: "z", Alpha: 0.5, Inner: &inner{Z: []any{1, "中"}, A: map[string]int{"y": 2, "b": 1}}}}
	before, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var rt map[string]any
	if err := json.Unmarshal(before, &rt); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(rt)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Fatalf("fixture no longer reproduces D5 key-order difference:\n%s", before)
	}
	norm, err := audit.NormalizeDetail(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(norm)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(after) {
		t.Fatalf("normalized detail must equal JSON round-trip form\n got %s\nwant %s", got, after)
	}
}
