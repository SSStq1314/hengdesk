// Package auth 实现本地账号登录与服务端会话（D-30）。
//
//   - 会话存放在 Valkey，cookie 只带随机 token（HttpOnly，Secure 可配置）。
//   - 连续失败锁定：默认 5 次 / 15 分钟，可配置。
//   - CSRF：同步令牌。登录时下发 hd_csrf cookie（JS 可读），变更类请求须带 X-CSRF-Token 头。
//   - 改密、停用、管理员重置密码时，该用户全部会话立即失效。
package auth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/session"
)

// CookieName 会话 cookie 名。
const CookieName = "hd_session"

// CSRFCookie 供前端读取的 CSRF 令牌 cookie。
const CSRFCookie = "hd_csrf"

// CSRFHeader 变更类请求必须携带的头。
const CSRFHeader = "X-CSRF-Token"

// MinPasswordLen 本地密码最小长度。
const MinPasswordLen = 8

const (
	ctxKey     = "hd_user"
	ctxSession = "hd_session"
	ctxToken   = "hd_token"
)

// dummyHash 用于账号不存在时做等时比较，避免用户枚举。
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("hengdesk-dummy"), bcrypt.DefaultCost)

// ErrUnavailable 会话存储不可用。
var ErrUnavailable = echo.NewHTTPError(http.StatusServiceUnavailable, "会话服务不可用，请稍后重试")

// Service 认证服务。
type Service struct {
	DB       *ent.Client
	Audit    audit.Writer
	Sessions *session.Store
	Lockout  *session.Lockout
	Secure   bool
	// BcryptCost 默认 bcrypt.DefaultCost，测试可调低。
	BcryptCost int
}

// Principal 当前登录用户。
type Principal struct {
	ID           int      `json:"id"`
	OrgID        int      `json:"org_id"`
	Email        string   `json:"email"`
	DisplayName  string   `json:"display_name"`
	IsSuperAdmin bool     `json:"is_super_admin"`
	Roles        []string `json:"roles"`
	CSRFToken    string   `json:"csrf_token,omitempty"`
}

// HasRole 是否拥有角色。
func (p *Principal) HasRole(keys ...string) bool {
	for _, r := range p.Roles {
		for _, k := range keys {
			if r == k {
				return true
			}
		}
	}
	return false
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Register 挂载 /auth 路由。
func (s *Service) Register(g *echo.Group) {
	g.POST("/auth/login", s.login)
	g.POST("/auth/logout", s.logout)
	g.GET("/auth/me", s.me, s.RequireUser)
	g.POST("/auth/password", s.changePassword, s.RequireUser)
}

func (s *Service) entry(c echo.Context, action, email string) audit.Entry {
	return audit.Entry{Action: action, ActorEmail: email, IP: c.RealIP(), UserAgent: c.Request().UserAgent(),
		RequestID: c.Response().Header().Get(echo.HeaderXRequestID), Outcome: "denied"}
}

func (s *Service) login(c echo.Context) error {
	var req loginReq
	if err := c.Bind(&req); err != nil || req.Email == "" || req.Password == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "请输入邮箱和密码")
	}
	ctx := c.Request().Context()
	email := strings.ToLower(strings.TrimSpace(req.Email))
	e := s.entry(c, "auth.login", email)

	left, err := s.Lockout.Locked(ctx, email)
	if err != nil {
		return ErrUnavailable
	}
	if left > 0 {
		e.Detail = map[string]any{"reason": "locked"}
		if aerr := s.Audit.Write(ctx, e); aerr != nil {
			return audit.ErrRejected
		}
		return echo.NewHTTPError(http.StatusTooManyRequests,
			fmt.Sprintf("登录失败次数过多，请 %d 分钟后再试", int(math.Ceil(left.Minutes()))))
	}

	u, err := s.DB.User.Query().Where(user.Email(email)).Only(ctx)
	ok := false
	if err == nil {
		ok = u.PasswordHash != "" && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) == nil && u.Status == user.StatusActive
	} else {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
	}
	if !ok {
		locked, lerr := s.Lockout.Fail(ctx, email)
		if lerr != nil {
			return ErrUnavailable
		}
		e.Detail = map[string]any{"reason": "bad_credentials", "locked": locked}
		if aerr := s.Audit.Write(ctx, e); aerr != nil {
			return audit.ErrRejected
		}
		return echo.NewHTTPError(http.StatusUnauthorized, "邮箱或密码错误")
	}
	e.Outcome, e.ActorID, e.OrgID = "success", u.ID, u.OrgID
	if aerr := s.Audit.Write(ctx, e); aerr != nil {
		return audit.ErrRejected
	}
	_ = s.Lockout.Reset(ctx, email)
	_ = s.DB.User.UpdateOne(u).SetLastLoginAt(time.Now()).Exec(ctx)
	return s.issue(c, u.ID)
}

// issue 建立新会话并下发 cookie，返回 Principal。
func (s *Service) issue(c echo.Context, uid int) error {
	tok, ss, err := s.Sessions.Create(c.Request().Context(), uid, c.RealIP(), c.Request().UserAgent())
	if err != nil {
		return ErrUnavailable
	}
	s.setCookies(c, tok, ss.CSRF, ss.ExpiresAt)
	p, err := s.principal(c, uid)
	if err != nil {
		return err
	}
	p.CSRFToken = ss.CSRF
	return c.JSON(http.StatusOK, p)
}

func (s *Service) setCookies(c echo.Context, tok, csrf string, exp time.Time) {
	c.SetCookie(&http.Cookie{Name: CookieName, Value: tok, Path: "/", HttpOnly: true, Secure: s.Secure,
		SameSite: http.SameSiteLaxMode, Expires: exp})
	c.SetCookie(&http.Cookie{Name: CSRFCookie, Value: csrf, Path: "/", HttpOnly: false, Secure: s.Secure,
		SameSite: http.SameSiteStrictMode, Expires: exp})
}

func (s *Service) clearCookies(c echo.Context) {
	for _, n := range []string{CookieName, CSRFCookie} {
		c.SetCookie(&http.Cookie{Name: n, Value: "", Path: "/", HttpOnly: n == CookieName, Secure: s.Secure, MaxAge: -1})
	}
}

func (s *Service) logout(c echo.Context) error {
	if tok, _ := c.Get(ctxToken).(string); tok != "" {
		if err := s.Sessions.Delete(c.Request().Context(), tok); err != nil {
			return ErrUnavailable
		}
	}
	s.clearCookies(c)
	return c.NoContent(http.StatusNoContent)
}

func (s *Service) me(c echo.Context) error {
	p := *Current(c)
	if ss := CurrentSession(c); ss != nil {
		p.CSRFToken = ss.CSRF
	}
	return c.JSON(http.StatusOK, p)
}

type pwReq struct {
	Current string `json:"current_password"`
	New     string `json:"new_password"`
}

// changePassword 本人改密：校验旧密码，成功后失效全部会话并为当前客户端签发新会话。
func (s *Service) changePassword(c echo.Context) error {
	var req pwReq
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	if err := ValidatePassword(req.New); err != nil {
		return err
	}
	ctx := c.Request().Context()
	p := Current(c)
	u, err := s.DB.User.Get(ctx, p.ID)
	if err != nil {
		return err
	}
	if u.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Current)) != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "当前密码不正确")
	}
	if err := s.SetPassword(c, u.ID, req.New); err != nil {
		return err
	}
	return s.issue(c, u.ID)
}

// ValidatePassword 本地密码策略。
func ValidatePassword(pw string) error {
	if len(pw) < MinPasswordLen {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("密码至少 %d 位", MinPasswordLen))
	}
	if len(pw) > 72 {
		return echo.NewHTTPError(http.StatusBadRequest, "密码最长 72 字节")
	}
	return nil
}

// SetPassword 更新密码哈希并失效该用户全部会话。
func (s *Service) SetPassword(c echo.Context, uid int, pw string) error {
	cost := s.BcryptCost
	if cost == 0 {
		cost = bcrypt.DefaultCost
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), cost)
	if err != nil {
		return err
	}
	if err := s.DB.User.UpdateOneID(uid).SetPasswordHash(string(h)).Exec(c.Request().Context()); err != nil {
		return err
	}
	return s.RevokeAll(c, uid)
}

// RevokeAll 失效用户全部会话（强制下线）。
func (s *Service) RevokeAll(c echo.Context, uid int) error {
	if _, err := s.Sessions.RevokeUser(c.Request().Context(), uid); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Service) principal(c echo.Context, id int) (*Principal, error) {
	u, err := s.DB.User.Query().Where(user.ID(id), user.StatusEQ(user.StatusActive)).
		WithMemberships(func(q *ent.MembershipQuery) { q.WithRole() }).Only(c.Request().Context())
	if err != nil {
		return nil, err
	}
	p := &Principal{ID: u.ID, OrgID: u.OrgID, Email: u.Email, DisplayName: u.DisplayName, IsSuperAdmin: u.IsSuperAdmin, Roles: []string{}}
	for _, m := range u.Edges.Memberships {
		if m.Edges.Role != nil {
			p.Roles = append(p.Roles, m.Edges.Role.Key)
		}
	}
	return p, nil
}

// Optional 若带有效会话则注入 Principal，不强制。会话存储故障时返回 503（不降级为匿名）。
func (s *Service) Optional(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ck, err := c.Cookie(CookieName)
		if err != nil || ck.Value == "" {
			return next(c)
		}
		ss, err := s.Sessions.Get(c.Request().Context(), ck.Value)
		if errors.Is(err, session.ErrNotFound) {
			return next(c)
		}
		if err != nil {
			return ErrUnavailable
		}
		p, err := s.principal(c, ss.UserID)
		if err != nil {
			// 用户已停用或删除：顺手清掉会话
			_, _ = s.Sessions.RevokeUser(c.Request().Context(), ss.UserID)
			return next(c)
		}
		c.Set(ctxKey, p)
		c.Set(ctxSession, ss)
		c.Set(ctxToken, ck.Value)
		return next(c)
	}
}

// CSRF 校验变更类请求：
//   - 已登录：X-CSRF-Token 必须与会话令牌一致；
//   - 未登录（如登录接口）：只接受 application/json，阻止跨站表单提交。
func CSRF(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		switch c.Request().Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return next(c)
		}
		if ss := CurrentSession(c); ss != nil {
			got := c.Request().Header.Get(CSRFHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(ss.CSRF)) != 1 {
				return echo.NewHTTPError(http.StatusForbidden, "CSRF 校验失败，请刷新页面后重试")
			}
			return next(c)
		}
		if ct := c.Request().Header.Get(echo.HeaderContentType); ct != "" {
			if mt, _, err := mime.ParseMediaType(ct); err == nil && mt == echo.MIMEApplicationJSON {
				return next(c)
			}
		}
		if c.Request().ContentLength == 0 && c.Request().Header.Get(echo.HeaderContentType) == "" {
			return next(c) // 无请求体（如未登录时的 logout）
		}
		return echo.NewHTTPError(http.StatusUnsupportedMediaType, "仅接受 JSON 请求")
	}
}

// RequireUser 未登录返回 401。
func (s *Service) RequireUser(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if Current(c) == nil {
			return echo.NewHTTPError(http.StatusUnauthorized, "未登录")
		}
		return next(c)
	}
}

// Current 当前用户，未登录返回 nil。
func Current(c echo.Context) *Principal {
	p, _ := c.Get(ctxKey).(*Principal)
	return p
}

// CurrentSession 当前会话。
func CurrentSession(c echo.Context) *session.Session {
	ss, _ := c.Get(ctxSession).(*session.Session)
	return ss
}

// Actor 供审计中间件使用。
func Actor(c echo.Context) (int, int, string) {
	if p := Current(c); p != nil {
		return p.OrgID, p.ID, p.Email
	}
	return 0, 0, ""
}
