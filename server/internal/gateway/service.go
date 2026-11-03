package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
)

// Service 把库里的模型 / 供应商配置解析成一次调用所需的参数。
type Service struct {
	DB     *ent.Client
	Keys   *Keyring
	Client *Client
}

// Target 一次调用的解析结果。
type Target struct {
	Model    *ent.Model
	Upstream Upstream
	Config   ModelConfig
}

// Resolve 按平台模型名解析；name 为空时使用默认模型。模型或供应商不存在 / 已停用时返回 model_not_found（调用方按 404 处理）。
func (s *Service) Resolve(ctx context.Context, name string) (*Target, *Error) {
	q := s.DB.Model.Query().WithProvider()
	if name == "" {
		q = q.Where(model.IsDefault(true)).Order(ent.Asc(model.FieldID)).Limit(1)
	} else {
		q = q.Where(model.Name(name))
	}
	ms, err := q.All(ctx)
	if err != nil || len(ms) == 0 {
		if name == "" {
			return nil, newErr(http.StatusNotFound, CodeModelNotFound, "未指定 model，且管理员没有设置默认模型")
		}
		return nil, newErr(http.StatusNotFound, CodeModelNotFound, "模型不存在或未启用")
	}
	return s.Target(ms[0], true)
}

// Target 把已加载 provider 边的模型解析成调用参数。requireEnabled=false 用于管理员「测试连接」（停用的模型也能测）。
func (s *Service) Target(m *ent.Model, requireEnabled bool) (*Target, *Error) {
	p := m.Edges.Provider
	if p == nil || (requireEnabled && (!m.Enabled || !p.Enabled)) {
		return nil, newErr(http.StatusNotFound, CodeModelNotFound, "模型不存在或未启用")
	}
	up, gerr := s.Upstream(p)
	if gerr != nil {
		return nil, gerr
	}
	return &Target{
		Model:    m,
		Upstream: *up,
		Config: ModelConfig{UpstreamModel: m.UpstreamModel, Thinking: string(m.Thinking),
			ToolChoicePolicy: string(m.ToolChoicePolicy), MaxOutputTokens: m.MaxOutputTokens},
	}, nil
}

// Upstream 解密供应商密钥，得到上游连接参数。
func (s *Service) Upstream(p *ent.ModelProvider) (*Upstream, *Error) {
	key := ""
	if len(p.APIKeyEnc) > 0 {
		k, err := s.Keys.Open(p.Name, p.APIKeyEnc)
		if err != nil {
			return nil, newErr(http.StatusServiceUnavailable, CodeKeyUnavailable, "无法解密该供应商的 API Key（主密钥未配置或不匹配），请联系管理员")
		}
		key = k
	}
	return &Upstream{Name: p.Name, BaseURL: p.BaseURL, APIKey: key, Kind: string(p.Kind), Dialect: string(p.Dialect),
		Timeout: time.Duration(p.TimeoutSeconds) * time.Second}, nil
}

// MaxFallbacks 每个模型最多配置的备用模型数。
const MaxFallbacks = 3

// Retry 重试策略（PR5）：同一上游最多尝试 PerTarget 次，之后按顺序切换备用模型；两次尝试之间退避 Backoff×n。
var Retry = struct {
	PerTarget int
	Backoff   time.Duration
}{PerTarget: 2, Backoff: 200 * time.Millisecond}

// ShouldRetry 只在首字节返回给调用方之前、且错误是连接错误 / 429 / 5xx 时重试。
// 流式调用已开始输出（started）或上游已经开始产生内容（e.Usage 非空）时一律不重试。
func ShouldRetry(e *Error, started bool) bool {
	return e != nil && e.Retryable && !started && e.Usage == nil
}

// Fallbacks 解析模型的备用模型（按配置顺序；跳过不存在、停用、自身或解析失败的条目）。
func (s *Service) Fallbacks(ctx context.Context, m *ent.Model) []*Target {
	var out []*Target
	for _, id := range m.FallbackModelIds {
		if id == m.ID || len(out) >= MaxFallbacks {
			continue
		}
		fm, err := s.DB.Model.Query().Where(model.ID(id)).WithProvider().Only(ctx)
		if err != nil {
			continue
		}
		t, gerr := s.Target(fm, true)
		if gerr != nil {
			continue
		}
		out = append(out, t)
	}
	return out
}
