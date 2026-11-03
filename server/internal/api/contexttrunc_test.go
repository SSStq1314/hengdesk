package api

import (
	"strings"
	"testing"
)

func TestEstimateTokens(t *testing.T) {
	if got := estimateTokens(strings.Repeat("中", 100)); got != 104 {
		t.Fatalf("CJK %d", got)
	}
	if got := estimateTokens(strings.Repeat("a", 400)); got != 104 {
		t.Fatalf("ascii %d", got)
	}
}

func TestTokenBudget(t *testing.T) {
	for _, c := range []struct{ win, out, want int }{{0, 100, 0}, {8000, 0, 8000 - outputReserveDefault}, {8000, 2000, 6000}, {1000, 1000, 250}} {
		if got := tokenBudget(c.win, c.out); got != c.want {
			t.Errorf("%+v got %d", c, got)
		}
	}
}

func TestTruncateByTokens(t *testing.T) {
	sys := ctxMsg{"system", strings.Repeat("文", 50)} // 54
	mk := func(role string, n int) ctxMsg { return ctxMsg{role, strings.Repeat("字", n-msgOverheadTokens)} }
	msgs := []ctxMsg{sys, mk("user", 100), mk("assistant", 100), mk("user", 100), mk("assistant", 100), mk("user", 50)}
	// 不截断
	if k, d, _ := truncateByTokens(msgs, 0); len(k) != 6 || d != 0 {
		t.Fatal("budget 0 不截断")
	}
	// 54 + 50 = 104，再放两条 100 → 304
	k, d, tok := truncateByTokens(msgs, 310)
	if d != 2 || len(k) != 4 || k[0].Role != "system" || k[len(k)-1].Content != msgs[5].Content || tok != 304 {
		t.Fatalf("kept=%d dropped=%d tok=%d", len(k), d, tok)
	}
	// 预算比系统 + 最新用户消息还小：仍保留这两条
	k, d, _ = truncateByTokens(msgs, 10)
	if len(k) != 2 || k[0].Role != "system" || k[1].Content != msgs[5].Content || d != 4 {
		t.Fatalf("kept=%d dropped=%d", len(k), d)
	}
	// 历史须连续：放不下一条后不再跳着补更早的短消息
	msgs2 := []ctxMsg{mk("user", 10), mk("assistant", 500), mk("user", 10)}
	if k, d, _ := truncateByTokens(msgs2, 100); len(k) != 1 || d != 2 {
		t.Fatalf("kept=%d dropped=%d", len(k), d)
	}
}
