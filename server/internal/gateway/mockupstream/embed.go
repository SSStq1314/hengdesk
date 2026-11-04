package mockupstream

import (
	"encoding/json"
	"hash/fnv"
	"math"
	"net/http"
	"strings"
	"unicode"
)

// 模拟 embedding 模型：mock-embed（1024 维）、mock-embed-768（768 维，用于维度不符的测试）。
// 向量是确定性的「词袋哈希」：ASCII 单词与相邻两个汉字各哈希到一个维度（带符号），再做 L2 归一化。
// 字面重合越多余弦相似度越高，足以在 CI 中验证检索、ACL 与引用链路；不代表真实语义效果。

// EmbedDims 模型名对应的维度；未知模型返回 0。
func EmbedDims(model string) int {
	switch model {
	case "mock-embed":
		return 1024
	case "mock-embed-768":
		return 768
	}
	return 0
}

// Tokens 词袋切分（与 HashEmbed 使用同一规则）。
func Tokens(text string) []string {
	var out []string
	var word []rune
	var prev rune
	flush := func() {
		if len(word) > 0 {
			out = append(out, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			out = append(out, string(r))
			if prev != 0 {
				out = append(out, string([]rune{prev, r}))
			}
			prev = r
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			word = append(word, r)
		default:
			flush()
		}
		prev = 0
	}
	flush()
	return out
}

// HashEmbed 生成 dim 维确定性向量。
func HashEmbed(text string, dim int) []float32 {
	v := make([]float64, dim)
	for _, t := range Tokens(text) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(t))
		x := h.Sum64()
		sign := 1.0
		if x&1 == 1 {
			sign = -1
		}
		v[(x>>1)%uint64(dim)] += sign
	}
	var n float64
	for _, f := range v {
		n += f * f
	}
	out := make([]float32, dim)
	if n == 0 {
		out[0] = 1
		return out
	}
	n = math.Sqrt(n)
	for i, f := range v {
		out[i] = float32(f / n)
	}
	return out
}

func (s *Server) embeddings(w http.ResponseWriter, r *http.Request) {
	if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != s.Key {
		writeErr(w, 401, "authentication_error", "Authentication Fails")
		return
	}
	var body struct {
		Model string `json:"model"`
		Input any    `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid_request_error", "invalid json")
		return
	}
	s.mu.Lock()
	s.EmbedCalls++
	fail := s.EmbedFail
	s.mu.Unlock()
	if fail != 0 {
		writeErr(w, fail, "server_error", "mock failure")
		return
	}
	dim := EmbedDims(body.Model)
	if dim == 0 {
		writeErr(w, 400, "invalid_request_error", "Model Not Exist. Supported: mock-embed, mock-embed-768")
		return
	}
	var inputs []string
	switch v := body.Input.(type) {
	case string:
		inputs = []string{v}
	case []any:
		for _, x := range v {
			sx, _ := x.(string)
			inputs = append(inputs, sx)
		}
	}
	data := make([]map[string]any, len(inputs))
	tokens := 0
	for i, in := range inputs {
		data[i] = map[string]any{"object": "embedding", "index": i, "embedding": HashEmbed(in, dim)}
		tokens += len(Tokens(in)) + 1
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "model": body.Model, "data": data,
		"usage": map[string]any{"prompt_tokens": tokens, "total_tokens": tokens}})
}
