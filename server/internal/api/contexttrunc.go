package api

import "unicode"

// D-40 第 4 条：按模型做 token 上下文截断。
//
// 预算 = 模型上下文窗口 − 最大输出 token（未配置时预留 outputReserveDefault）。
// 上下文窗口未配置（0）时不按 token 截断，只保留固定的最近 50 条。
// 截断时总是保留系统提示词（role=system，含文件上下文）与最新一条用户消息，再从新到旧补历史，放不下的更早消息丢弃。
//
// token 数为启发式估算（不依赖各家 tokenizer）：中日韩字符按 1 个 token，其他字符按 4 个字符 1 个 token，
// 每条消息另加 4 个 token 的格式开销。估算偏保守，宁可多截不让上游报超长。
const (
	outputReserveDefault = 1024
	msgOverheadTokens    = 4
)

type ctxMsg struct {
	Role    string
	Content string
}

func estimateTokens(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF) {
			cjk++
		} else {
			other++
		}
	}
	return cjk + (other+3)/4 + msgOverheadTokens
}

// tokenBudget 返回可用于输入的 token 预算；0 表示不截断。
func tokenBudget(contextWindow, maxOutput int) int {
	if contextWindow <= 0 {
		return 0
	}
	reserve := maxOutput
	if reserve <= 0 {
		reserve = outputReserveDefault
	}
	b := contextWindow - reserve
	if b < contextWindow/4 { // 配置异常（输出占满窗口）时至少留 1/4 给输入
		b = contextWindow / 4
	}
	return b
}

// truncateByTokens 按预算截断。msgs 为时间正序（系统消息在前）。返回保留的消息、丢弃的历史条数、估算 token。
func truncateByTokens(msgs []ctxMsg, budget int) (kept []ctxMsg, dropped, tokens int) {
	if budget <= 0 {
		for _, m := range msgs {
			tokens += estimateTokens(m.Content)
		}
		return msgs, 0, tokens
	}
	lastUser := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUser = i
			break
		}
	}
	keep := make([]bool, len(msgs))
	for i, m := range msgs {
		if m.Role == "system" || i == lastUser {
			keep[i] = true
			tokens += estimateTokens(m.Content)
		}
	}
	full := false
	for i := len(msgs) - 1; i >= 0; i-- {
		if keep[i] {
			continue
		}
		t := estimateTokens(msgs[i].Content)
		if !full && tokens+t <= budget {
			keep[i] = true
			tokens += t
			continue
		}
		full = true // 一旦放不下就停止，保证保留的历史是连续的最近一段
		dropped++
	}
	kept = make([]ctxMsg, 0, len(msgs)-dropped)
	for i, m := range msgs {
		if keep[i] {
			kept = append(kept, m)
		}
	}
	return kept, dropped, tokens
}
