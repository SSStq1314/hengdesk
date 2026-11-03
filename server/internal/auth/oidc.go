package auth

// OIDC 单点登录：authorization code + PKCE（S256），基于 coreos/go-oidc（Apache-2.0）与 golang.org/x/oauth2（BSD-3-Clause）。
//
//   - state / nonce / code_verifier 存 Valkey（10 分钟、一次性），state 同时写入浏览器 cookie，回调时两者必须一致。
//   - 关联本地用户：先按 (issuer, sub)；LinkBy=email 时再按「已验证」邮箱匹配未关联 SSO 的本地账号并写入 sub。
//   - 自动建号（AutoCreate，D-34 默认开启）只在邮箱域名白名单非空且命中时生效，只给「成员」角色。
//   - 特权账号（超级管理员 / 安全审计员）不按邮箱自动关联，拒绝并记审计；只能由管理员手动绑定。
//   - 部门按 claim 映射（path / code），映射不到归入默认部门；每次登录同步，变化时记审计。
//   - 成功、拒绝、关联、建号都写审计，且先审计后签发会话（fail-closed）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/egress"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"golang.org/x/oauth2"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/organization"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/session"
)

// OIDCStateCookie 绑定浏览器的 state cookie。
const OIDCStateCookie = "hd_oidc_state"

const oidcStateTTL = 10 * time.Minute

// OIDCConfig OIDC 配置；Issuer 为空表示未启用。
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	// LinkBy 关联方式：email（先 sub、再已验证邮箱）或 sub（只认 sub）。
	LinkBy string
	// AutoCreate 无匹配本地账号时是否自动建号（默认 false）。
	AutoCreate bool
	// EmailDomains 邮箱域名白名单（小写、不含 @）。为空时不自动建号；非空时按邮箱关联也须命中。
	EmailDomains []string
	// DeptClaim 部门 claim 名（字符串或字符串数组）；为空不映射。
	DeptClaim string
	// DeptMatch path：按部门名路径（如 研发部/后端组）匹配；code：按部门编码（external_id）匹配。
	DeptMatch string
	// DeptDefault 映射不到时归入的部门（写法同 DeptMatch）；为空表示不变。
	DeptDefault string
	// OrgSlug 自动建号所属组织。
	OrgSlug string
	// DisplayName 登录页按钮文案。
	DisplayName string
	// HTTPClient 访问 IdP 用（测试注入）。
	HTTPClient *http.Client
}

// OIDC 运行时状态（Provider 懒加载，IdP 暂不可达时下次重试）。
type OIDC struct {
	Cfg OIDCConfig
	R   redis.UniversalClient

	mu       sync.Mutex
	provider *oidc.Provider
}

type oidcState struct {
	Verifier string `json:"v"`
	Nonce    string `json:"n"`
}

func (o *OIDC) ctx(ctx context.Context) context.Context {
	if o.Cfg.HTTPClient != nil {
		return oidc.ClientContext(egress.WithCaller(ctx, egress.Caller{Module: "oidc"}), o.Cfg.HTTPClient)
	}
	return ctx
}

func (o *OIDC) init(ctx context.Context) (*oidc.Provider, *oauth2.Config, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider == nil {
		p, err := oidc.NewProvider(o.ctx(ctx), o.Cfg.Issuer)
		if err != nil {
			return nil, nil, err
		}
		o.provider = p
	}
	scopes := o.Cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	return o.provider, &oauth2.Config{ClientID: o.Cfg.ClientID, ClientSecret: o.Cfg.ClientSecret,
		RedirectURL: o.Cfg.RedirectURL, Endpoint: o.provider.Endpoint(), Scopes: scopes}, nil
}

func (s *Service) registerOIDC(g *echo.Group) {
	g.GET("/auth/oidc/config", s.oidcConfig)
	if s.OIDC == nil {
		return
	}
	g.GET("/auth/oidc/login", s.oidcLogin)
	g.GET("/auth/oidc/callback", s.oidcCallback)
}

func (s *Service) oidcConfig(c echo.Context) error {
	if s.OIDC == nil {
		return c.JSON(http.StatusOK, map[string]any{"enabled": false})
	}
	name := s.OIDC.Cfg.DisplayName
	if name == "" {
		name = "企业单点登录"
	}
	return c.JSON(http.StatusOK, map[string]any{"enabled": true, "display_name": name, "enforced": s.EnforceSSO})
}

func (s *Service) oidcLogin(c echo.Context) error {
	ctx := c.Request().Context()
	_, oc, err := s.OIDC.init(ctx)
	if err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "单点登录服务暂不可用")
	}
	state, err1 := session.RandomToken()
	nonce, err2 := session.RandomToken()
	if err1 != nil || err2 != nil {
		return ErrUnavailable
	}
	verifier := oauth2.GenerateVerifier()
	b, _ := json.Marshal(oidcState{Verifier: verifier, Nonce: nonce})
	if err := s.OIDC.R.Set(ctx, "oidc:state:"+state, b, oidcStateTTL).Err(); err != nil {
		return ErrUnavailable
	}
	c.SetCookie(&http.Cookie{Name: OIDCStateCookie, Value: state, Path: "/api/v1/auth/oidc", HttpOnly: true,
		Secure: s.Secure, SameSite: http.SameSiteLaxMode, MaxAge: int(oidcStateTTL.Seconds())})
	return c.Redirect(http.StatusFound, oc.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)))
}

// oidcFail 写拒绝审计并跳回登录页（不暴露具体原因）。
func (s *Service) oidcFail(c echo.Context, e audit.Entry, reason string) error {
	e.Detail = map[string]any{"method": "oidc", "reason": reason}
	if err := s.Audit.Write(c.Request().Context(), e); err != nil {
		return audit.ErrRejected
	}
	return c.Redirect(http.StatusFound, "/sign-in?error=sso")
}

type oidcClaims struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified *bool  `json:"email_verified"`
	Name          string `json:"name"`
}

func (s *Service) oidcCallback(c echo.Context) error {
	ctx := c.Request().Context()
	e := s.entry(c, "auth.login", "")
	c.SetCookie(&http.Cookie{Name: OIDCStateCookie, Value: "", Path: "/api/v1/auth/oidc", HttpOnly: true, Secure: s.Secure, MaxAge: -1})

	if okIP, _, err := s.IPLimit.Allow(ctx, "login:ip:"+c.RealIP()); err != nil {
		return ErrUnavailable
	} else if !okIP {
		return s.oidcFail(c, e, "ip_rate_limited")
	}
	if c.QueryParam("error") != "" {
		return s.oidcFail(c, e, "idp_error")
	}
	state := c.QueryParam("state")
	ck, err := c.Cookie(OIDCStateCookie)
	if state == "" || err != nil || ck.Value != state {
		return s.oidcFail(c, e, "state_mismatch")
	}
	raw, err := s.OIDC.R.GetDel(ctx, "oidc:state:"+state).Bytes()
	if errors.Is(err, redis.Nil) {
		return s.oidcFail(c, e, "state_expired")
	} else if err != nil {
		return ErrUnavailable
	}
	var st oidcState
	if json.Unmarshal(raw, &st) != nil {
		return s.oidcFail(c, e, "state_invalid")
	}
	p, oc, err := s.OIDC.init(ctx)
	if err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "单点登录服务暂不可用")
	}
	tok, err := oc.Exchange(s.OIDC.ctx(ctx), c.QueryParam("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return s.oidcFail(c, e, "code_exchange")
	}
	rawID, _ := tok.Extra("id_token").(string)
	idt, err := p.Verifier(&oidc.Config{ClientID: s.OIDC.Cfg.ClientID}).Verify(s.OIDC.ctx(ctx), rawID)
	if err != nil {
		return s.oidcFail(c, e, "id_token_invalid")
	}
	if idt.Nonce != st.Nonce {
		return s.oidcFail(c, e, "nonce_mismatch")
	}
	var cl oidcClaims
	all := map[string]any{}
	if err := idt.Claims(&cl); err != nil || cl.Sub == "" || idt.Claims(&all) != nil {
		return s.oidcFail(c, e, "claims_invalid")
	}
	if dc := s.OIDC.Cfg.DeptClaim; dc != "" {
		if _, ok := all[dc]; !ok { // 部门 claim 不在 ID Token 时再查 UserInfo
			if ui, uerr := p.UserInfo(s.OIDC.ctx(ctx), oauth2.StaticTokenSource(tok)); uerr == nil {
				_ = ui.Claims(&all)
			}
		}
	}
	cl.Email = strings.ToLower(strings.TrimSpace(cl.Email))
	e.ActorEmail = cl.Email
	u, err := s.oidcResolve(c, e, idt.Issuer, cl)
	var d oidcDenial
	if errors.As(err, &d) {
		return s.oidcFail(c, e, d.reason)
	} else if err != nil {
		return err
	}
	if u == nil {
		return s.oidcFail(c, e, "no_local_account")
	}
	if u.Status != user.StatusActive {
		return s.oidcFail(c, e, "disabled")
	}
	if s.OIDC.Cfg.DeptClaim != "" {
		if err := s.oidcSyncDepts(c, e, u, claimValues(all[s.OIDC.Cfg.DeptClaim])); err != nil {
			return err
		}
	}
	e.Outcome, e.ActorID, e.OrgID, e.ActorEmail = "success", u.ID, u.OrgID, u.Email
	e.Detail = map[string]any{"method": "oidc", "issuer": idt.Issuer}
	if err := s.Audit.Write(ctx, e); err != nil {
		return audit.ErrRejected
	}
	_ = s.DB.User.UpdateOne(u).SetLastLoginAt(time.Now()).Exec(ctx)
	sessTok, ss, err := s.Sessions.Create(ctx, u.ID, c.RealIP(), c.Request().UserAgent())
	if err != nil {
		return ErrUnavailable
	}
	s.setCookies(c, sessTok, ss.CSRF, ss.ExpiresAt)
	return c.Redirect(http.StatusFound, "/")
}

// oidcResolve 按配置找到（或关联、创建）本地用户；找不到且不允许建号时返回 nil, nil。
func (s *Service) oidcResolve(c echo.Context, e audit.Entry, iss string, cl oidcClaims) (*ent.User, error) {
	ctx := c.Request().Context()
	u, err := s.DB.User.Query().Where(user.OidcIssuer(iss), user.OidcSubject(cl.Sub)).Only(ctx)
	if err == nil {
		return u, nil
	} else if !ent.IsNotFound(err) {
		return nil, err
	}
	verified := cl.EmailVerified != nil && *cl.EmailVerified
	if s.OIDC.Cfg.LinkBy != "sub" && cl.Email != "" && verified {
		u, err := s.DB.User.Query().Where(user.Email(cl.Email)).Only(ctx)
		if err == nil {
			if u.OidcSubject != "" { // 已关联其他 SSO 身份，拒绝改绑
				return nil, nil
			}
			if len(s.OIDC.Cfg.EmailDomains) > 0 && !s.OIDC.domainAllowed(cl.Email) {
				return nil, oidcDenial{"email_domain_not_allowed"}
			}
			if priv, err := IsPrivileged(ctx, s.DB, u.ID); err != nil {
				return nil, err
			} else if priv { // D-34：特权账号不按邮箱自动合并，只能由管理员手动绑定
				return nil, oidcDenial{"privileged_link_refused"}
			}
			le := e
			le.Action, le.Outcome, le.ActorID, le.OrgID = "auth.oidc.link", "success", u.ID, u.OrgID
			le.ResourceType, le.ResourceID = "user", u.Email
			le.Detail = map[string]any{"issuer": iss, "by": "email"}
			if err := s.Audit.Write(ctx, le); err != nil {
				return nil, audit.ErrRejected
			}
			return s.DB.User.UpdateOne(u).SetOidcIssuer(iss).SetOidcSubject(cl.Sub).Save(ctx)
		} else if !ent.IsNotFound(err) {
			return nil, err
		}
	}
	if !s.OIDC.Cfg.AutoCreate || cl.Email == "" || !verified {
		return nil, nil
	}
	if !s.OIDC.domainAllowed(cl.Email) { // 白名单为空一律不建号
		return nil, oidcDenial{"email_domain_not_allowed"}
	}
	if exists, err := s.DB.User.Query().Where(user.Email(cl.Email)).Exist(ctx); err != nil || exists {
		return nil, err // LinkBy=sub 时同邮箱本地账号存在：不自动合并
	}
	org, err := s.DB.Organization.Query().Where(organization.Slug(s.OIDC.Cfg.OrgSlug)).Only(ctx)
	if err != nil {
		return nil, err
	}
	mr, err := s.DB.Role.Query().Where(role.Key(rbac.Member)).Only(ctx)
	if err != nil {
		return nil, err
	}
	ce := e
	ce.Action, ce.Outcome, ce.OrgID, ce.ResourceType, ce.ResourceID = "user.create", "success", org.ID, "user", cl.Email
	ce.Detail = map[string]any{"via": "oidc_auto_create", "issuer": iss, "role": rbac.Member}
	if err := s.Audit.Write(ctx, ce); err != nil {
		return nil, audit.ErrRejected
	}
	name := cl.Name
	if name == "" {
		name = cl.Email
	}
	u, err = s.DB.User.Create().SetEmail(cl.Email).SetDisplayName(name).SetOrg(org).
		SetOidcIssuer(iss).SetOidcSubject(cl.Sub).Save(ctx)
	if err != nil {
		return nil, err
	}
	return u, s.DB.Membership.Create().SetUser(u).SetRole(mr).SetIsPrimary(true).Exec(ctx)
}
