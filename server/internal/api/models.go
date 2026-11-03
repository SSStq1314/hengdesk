package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/ent/modelprovider"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
)

// 模型网关管理（FR-09/10 的一部分）：供应商与模型 CRUD。
// 上游 API Key 只写不读：接口只返回 has_api_key，不返回明文、密文或片段。
type modelHandlers struct {
	db *ent.Client
	gw *gateway.Service
}

type providerView struct {
	ID             int       `json:"id"`
	Name           string    `json:"name"`
	Kind           string    `json:"kind"`
	Dialect        string    `json:"dialect"`
	BaseURL        string    `json:"base_url"`
	HasAPIKey      bool      `json:"has_api_key"`
	Enabled        bool      `json:"enabled"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func toProviderView(p *ent.ModelProvider) providerView {
	return providerView{ID: p.ID, Name: p.Name, Kind: string(p.Kind), Dialect: string(p.Dialect), BaseURL: p.BaseURL,
		HasAPIKey: len(p.APIKeyEnc) > 0, Enabled: p.Enabled, TimeoutSeconds: p.TimeoutSeconds,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

type modelView struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	ProviderID       int    `json:"provider_id"`
	UpstreamModel    string `json:"upstream_model"`
	Thinking         string `json:"thinking"`
	ToolChoicePolicy string `json:"tool_choice_policy"`
	ContextWindow    int    `json:"context_window"`
	MaxOutputTokens  int    `json:"max_output_tokens"`
	Enabled          bool   `json:"enabled"`
}

func toModelView(m *ent.Model) modelView {
	return modelView{ID: m.ID, Name: m.Name, ProviderID: m.ProviderID, UpstreamModel: m.UpstreamModel,
		Thinking: string(m.Thinking), ToolChoicePolicy: string(m.ToolChoicePolicy), ContextWindow: m.ContextWindow,
		MaxOutputTokens: m.MaxOutputTokens, Enabled: m.Enabled}
}

func bad(msg string) error { return echo.NewHTTPError(http.StatusBadRequest, msg) }

func validBaseURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return bad("base_url 必须是 http(s)://host[:port][/path]，且不能包含用户名密码")
	}
	return nil
}

func validName(s string) error {
	if s == "" || len(s) > 64 || strings.TrimSpace(s) != s {
		return bad("name 不能为空、不能超过 64 字符、首尾不能有空格")
	}
	return nil
}

func idParam(c echo.Context) (int, error) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return 0, authz.ErrNotFound
	}
	return id, nil
}

func notFoundOr(err error) error {
	if ent.IsNotFound(err) {
		return authz.ErrNotFound
	}
	if ent.IsConstraintError(err) {
		return echo.NewHTTPError(http.StatusConflict, "名称已存在或仍被引用")
	}
	return err
}

func (h *modelHandlers) record(c echo.Context, action, resType string, id int, detail map[string]any) error {
	return audit.Record(c, audit.Entry{Action: action, Phase: "result", Outcome: "ok",
		ResourceType: resType, ResourceID: strconv.Itoa(id), Detail: detail})
}

func (h *modelHandlers) listProviders(c echo.Context) error {
	ps, err := h.db.ModelProvider.Query().Order(ent.Asc(modelprovider.FieldID)).All(c.Request().Context())
	if err != nil {
		return err
	}
	out := make([]providerView, len(ps))
	for i, p := range ps {
		out[i] = toProviderView(p)
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

type providerInput struct {
	Name           *string `json:"name"`
	Dialect        *string `json:"dialect"`
	BaseURL        *string `json:"base_url"`
	APIKey         *string `json:"api_key"`
	ClearAPIKey    bool    `json:"clear_api_key"`
	Enabled        *bool   `json:"enabled"`
	TimeoutSeconds *int    `json:"timeout_seconds"`
}

func (in *providerInput) validate() error {
	if in.Name != nil {
		if err := validName(*in.Name); err != nil {
			return err
		}
	}
	if in.BaseURL != nil {
		if err := validBaseURL(*in.BaseURL); err != nil {
			return err
		}
	}
	if in.Dialect != nil && modelprovider.DialectValidator(modelprovider.Dialect(*in.Dialect)) != nil {
		return bad("dialect 只能是 generic 或 deepseek")
	}
	if in.TimeoutSeconds != nil && (*in.TimeoutSeconds < 1 || *in.TimeoutSeconds > 3600) {
		return bad("timeout_seconds 范围 1~3600")
	}
	if in.APIKey != nil && len(*in.APIKey) > 4096 {
		return bad("api_key 过长")
	}
	return nil
}

func (h *modelHandlers) seal(name, key string) ([]byte, error) {
	b, err := h.gw.Keys.Seal(name, key)
	if errors.Is(err, gateway.ErrNoMasterKey) {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "未配置主密钥，不能保存上游 API Key（见 docs/ops/model-gateway.md）")
	}
	return b, err
}

func (h *modelHandlers) createProvider(c echo.Context) error {
	var in providerInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	if in.Name == nil || in.BaseURL == nil {
		return bad("name 和 base_url 必填")
	}
	if err := in.validate(); err != nil {
		return err
	}
	q := h.db.ModelProvider.Create().SetName(*in.Name).SetBaseURL(*in.BaseURL)
	if in.Dialect != nil {
		q.SetDialect(modelprovider.Dialect(*in.Dialect))
	}
	if in.Enabled != nil {
		q.SetEnabled(*in.Enabled)
	}
	if in.TimeoutSeconds != nil {
		q.SetTimeoutSeconds(*in.TimeoutSeconds)
	}
	if in.APIKey != nil && *in.APIKey != "" {
		enc, err := h.seal(*in.Name, *in.APIKey)
		if err != nil {
			return err
		}
		q.SetAPIKeyEnc(enc)
	}
	p, err := q.Save(c.Request().Context())
	if err != nil {
		return notFoundOr(err)
	}
	if err := h.record(c, "model.provider.create", "model_provider", p.ID, map[string]any{
		"name": p.Name, "base_url": p.BaseURL, "dialect": string(p.Dialect), "api_key_set": len(p.APIKeyEnc) > 0}); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toProviderView(p))
}

func (h *modelHandlers) updateProvider(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	p, err := h.db.ModelProvider.Get(ctx, id)
	if err != nil {
		return notFoundOr(err)
	}
	var in providerInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	if err := in.validate(); err != nil {
		return err
	}
	u := p.Update()
	name := p.Name
	if in.Name != nil {
		name = *in.Name
		u.SetName(name)
	}
	if in.BaseURL != nil {
		u.SetBaseURL(*in.BaseURL)
	}
	if in.Dialect != nil {
		u.SetDialect(modelprovider.Dialect(*in.Dialect))
	}
	if in.Enabled != nil {
		u.SetEnabled(*in.Enabled)
	}
	if in.TimeoutSeconds != nil {
		u.SetTimeoutSeconds(*in.TimeoutSeconds)
	}
	keyChange := "unchanged"
	switch {
	case in.ClearAPIKey:
		u.ClearAPIKeyEnc()
		keyChange = "cleared"
	case in.APIKey != nil && *in.APIKey != "":
		enc, err := h.seal(name, *in.APIKey)
		if err != nil {
			return err
		}
		u.SetAPIKeyEnc(enc)
		keyChange = "replaced"
	case name != p.Name && len(p.APIKeyEnc) > 0:
		// 密文的 AAD 绑定供应商名：改名时用新名字重新加密。
		pt, err := h.gw.Keys.Open(p.Name, p.APIKeyEnc)
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "无法解密现有 API Key，改名时请同时重新填写 api_key")
		}
		enc, err := h.seal(name, pt)
		if err != nil {
			return err
		}
		u.SetAPIKeyEnc(enc)
	}
	p, err = u.Save(ctx)
	if err != nil {
		return notFoundOr(err)
	}
	if err := h.record(c, "model.provider.update", "model_provider", p.ID, map[string]any{
		"name": p.Name, "base_url": p.BaseURL, "enabled": p.Enabled, "api_key": keyChange}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toProviderView(p))
}

func (h *modelHandlers) deleteProvider(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	p, err := h.db.ModelProvider.Get(ctx, id)
	if err != nil {
		return notFoundOr(err)
	}
	if n, _ := h.db.Model.Query().Where(model.ProviderID(id)).Count(ctx); n > 0 {
		return echo.NewHTTPError(http.StatusConflict, "该供应商下还有模型，请先删除模型")
	}
	if err := h.db.ModelProvider.DeleteOne(p).Exec(ctx); err != nil {
		return notFoundOr(err)
	}
	if err := h.record(c, "model.provider.delete", "model_provider", id, map[string]any{"name": p.Name}); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *modelHandlers) listModels(c echo.Context) error {
	ms, err := h.db.Model.Query().Order(ent.Asc(model.FieldID)).All(c.Request().Context())
	if err != nil {
		return err
	}
	out := make([]modelView, len(ms))
	for i, m := range ms {
		out[i] = toModelView(m)
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

type modelInput struct {
	Name             *string `json:"name"`
	ProviderID       *int    `json:"provider_id"`
	UpstreamModel    *string `json:"upstream_model"`
	Thinking         *string `json:"thinking"`
	ToolChoicePolicy *string `json:"tool_choice_policy"`
	ContextWindow    *int    `json:"context_window"`
	MaxOutputTokens  *int    `json:"max_output_tokens"`
	Enabled          *bool   `json:"enabled"`
}

func (in *modelInput) validate() error {
	if in.Name != nil {
		if err := validName(*in.Name); err != nil {
			return err
		}
	}
	if in.UpstreamModel != nil && (*in.UpstreamModel == "" || len(*in.UpstreamModel) > 128) {
		return bad("upstream_model 不能为空且不超过 128 字符")
	}
	if in.Thinking != nil && model.ThinkingValidator(model.Thinking(*in.Thinking)) != nil {
		return bad("thinking 只能是 off / on / provider_default")
	}
	if in.ToolChoicePolicy != nil && model.ToolChoicePolicyValidator(model.ToolChoicePolicy(*in.ToolChoicePolicy)) != nil {
		return bad("tool_choice_policy 只能是 downgrade / reject")
	}
	if (in.ContextWindow != nil && *in.ContextWindow < 0) || (in.MaxOutputTokens != nil && *in.MaxOutputTokens < 0) {
		return bad("context_window / max_output_tokens 不能为负")
	}
	return nil
}

func (h *modelHandlers) createModel(c echo.Context) error {
	var in modelInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	if in.Name == nil || in.ProviderID == nil || in.UpstreamModel == nil {
		return bad("name、provider_id、upstream_model 必填")
	}
	if err := in.validate(); err != nil {
		return err
	}
	ctx := c.Request().Context()
	if ok, _ := h.db.ModelProvider.Query().Where(modelprovider.ID(*in.ProviderID)).Exist(ctx); !ok {
		return bad("provider_id 不存在")
	}
	q := h.db.Model.Create().SetName(*in.Name).SetProviderID(*in.ProviderID).SetUpstreamModel(*in.UpstreamModel)
	if in.Thinking != nil {
		q.SetThinking(model.Thinking(*in.Thinking))
	}
	if in.ToolChoicePolicy != nil {
		q.SetToolChoicePolicy(model.ToolChoicePolicy(*in.ToolChoicePolicy))
	}
	if in.ContextWindow != nil {
		q.SetContextWindow(*in.ContextWindow)
	}
	if in.MaxOutputTokens != nil {
		q.SetMaxOutputTokens(*in.MaxOutputTokens)
	}
	if in.Enabled != nil {
		q.SetEnabled(*in.Enabled)
	}
	m, err := q.Save(ctx)
	if err != nil {
		return notFoundOr(err)
	}
	if err := h.record(c, "model.create", "model", m.ID, map[string]any{"name": m.Name, "provider_id": m.ProviderID,
		"upstream_model": m.UpstreamModel, "thinking": string(m.Thinking)}); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toModelView(m))
}

func (h *modelHandlers) updateModel(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	m, err := h.db.Model.Get(ctx, id)
	if err != nil {
		return notFoundOr(err)
	}
	var in modelInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	if err := in.validate(); err != nil {
		return err
	}
	u := m.Update()
	if in.Name != nil {
		u.SetName(*in.Name)
	}
	if in.ProviderID != nil {
		if ok, _ := h.db.ModelProvider.Query().Where(modelprovider.ID(*in.ProviderID)).Exist(ctx); !ok {
			return bad("provider_id 不存在")
		}
		u.SetProviderID(*in.ProviderID)
	}
	if in.UpstreamModel != nil {
		u.SetUpstreamModel(*in.UpstreamModel)
	}
	if in.Thinking != nil {
		u.SetThinking(model.Thinking(*in.Thinking))
	}
	if in.ToolChoicePolicy != nil {
		u.SetToolChoicePolicy(model.ToolChoicePolicy(*in.ToolChoicePolicy))
	}
	if in.ContextWindow != nil {
		u.SetContextWindow(*in.ContextWindow)
	}
	if in.MaxOutputTokens != nil {
		u.SetMaxOutputTokens(*in.MaxOutputTokens)
	}
	if in.Enabled != nil {
		u.SetEnabled(*in.Enabled)
	}
	m, err = u.Save(ctx)
	if err != nil {
		return notFoundOr(err)
	}
	if err := h.record(c, "model.update", "model", m.ID, map[string]any{"name": m.Name, "enabled": m.Enabled,
		"upstream_model": m.UpstreamModel, "thinking": string(m.Thinking)}); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toModelView(m))
}

func (h *modelHandlers) deleteModel(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	m, err := h.db.Model.Get(ctx, id)
	if err != nil {
		return notFoundOr(err)
	}
	if err := h.db.Model.DeleteOne(m).Exec(ctx); err != nil {
		return notFoundOr(err)
	}
	if err := h.record(c, "model.delete", "model", id, map[string]any{"name": m.Name}); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
