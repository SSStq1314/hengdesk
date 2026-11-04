package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// EmbedResult embedding 调用结果。Vectors 与输入一一对应（按上游返回的 index 排序）。
type EmbedResult struct {
	Vectors [][]float32
	Usage   Usage
}

// Embed 调用 OpenAI 兼容的 POST {base_url}/embeddings（知识库索引与检索，M1 第四块）。
// Anthropic 没有 embedding 接口，直接返回 bad_request。上游没给 usage 时按输入字符估算（Estimated=true）。
func (c *Client) Embed(ctx context.Context, up Upstream, upstreamModel string, inputs []string) (*EmbedResult, *Error) {
	if up.Kind == KindAnthropic {
		return nil, newErr(http.StatusBadRequest, CodeBadRequest, "Anthropic 协议的供应商不支持 embedding，请为知识库选择 OpenAI 兼容的 embedding 模型")
	}
	if len(inputs) == 0 {
		return &EmbedResult{}, nil
	}
	b, err := json.Marshal(map[string]any{"model": upstreamModel, "input": inputs, "encoding_format": "float"})
	if err != nil {
		return nil, newErr(http.StatusBadRequest, CodeBadRequest, "请求无法序列化")
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutOf(up))
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(up.BaseURL, "/")+"/embeddings", bytes.NewReader(b))
	if err != nil {
		return nil, newErr(http.StatusBadGateway, CodeUpstreamUnavailable, "上游地址无效")
	}
	r.Header.Set("Content-Type", "application/json")
	if up.APIKey != "" {
		r.Header.Set("Authorization", "Bearer "+up.APIKey)
	}
	resp, gerr := c.do(ctx, r, up.APIKey)
	if gerr != nil {
		return nil, gerr
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, upstreamError(resp.StatusCode, raw, up.APIKey)
	}
	var w struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage *wireUsage `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(&w); err != nil {
		if ctx.Err() != nil {
			return nil, newErr(http.StatusGatewayTimeout, CodeUpstreamTimeout, "上游响应超时")
		}
		return nil, newErr(http.StatusBadGateway, CodeUpstreamProtocol, "上游 /embeddings 返回无法解析")
	}
	if len(w.Data) != len(inputs) {
		return nil, newErr(http.StatusBadGateway, CodeUpstreamProtocol, "上游 /embeddings 返回条数与输入不符")
	}
	out := &EmbedResult{Vectors: make([][]float32, len(inputs))}
	for _, d := range w.Data {
		if d.Index < 0 || d.Index >= len(inputs) || out.Vectors[d.Index] != nil || len(d.Embedding) == 0 {
			return nil, newErr(http.StatusBadGateway, CodeUpstreamProtocol, "上游 /embeddings 返回的 index 无效")
		}
		out.Vectors[d.Index] = d.Embedding
	}
	if w.Usage != nil && (w.Usage.PromptTokens > 0 || w.Usage.TotalTokens > 0) {
		out.Usage = w.Usage.toUsage()
		if out.Usage.PromptTokens == 0 {
			out.Usage.PromptTokens = out.Usage.TotalTokens
		}
	} else {
		n := 0
		for _, s := range inputs {
			n += EstimateText(s)
		}
		out.Usage = Usage{PromptTokens: n, TotalTokens: n, Estimated: true}
	}
	return out, nil
}
