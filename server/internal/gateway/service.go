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

// Resolve 按平台模型名解析；模型或供应商不存在 / 已停用时返回 model_not_found（调用方按 404 处理）。
func (s *Service) Resolve(ctx context.Context, name string) (*Target, *Error) {
	m, err := s.DB.Model.Query().Where(model.Name(name)).WithProvider().Only(ctx)
	if err != nil || !m.Enabled || m.Edges.Provider == nil || !m.Edges.Provider.Enabled {
		return nil, newErr(http.StatusNotFound, CodeModelNotFound, "模型不存在或未启用")
	}
	p := m.Edges.Provider
	key := ""
	if len(p.APIKeyEnc) > 0 {
		k, err := s.Keys.Open(p.Name, p.APIKeyEnc)
		if err != nil {
			return nil, newErr(http.StatusServiceUnavailable, CodeKeyUnavailable, "无法解密该供应商的 API Key（主密钥未配置或不匹配），请联系管理员")
		}
		key = k
	}
	return &Target{
		Model: m,
		Upstream: Upstream{Name: p.Name, BaseURL: p.BaseURL, APIKey: key, Dialect: string(p.Dialect),
			Timeout: time.Duration(p.TimeoutSeconds) * time.Second},
		Config: ModelConfig{UpstreamModel: m.UpstreamModel, Thinking: string(m.Thinking),
			ToolChoicePolicy: string(m.ToolChoicePolicy), MaxOutputTokens: m.MaxOutputTokens},
	}, nil
}
