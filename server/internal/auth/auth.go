// Package auth 实现 M0 本地账号登录：bcrypt 校验 + HS256 JWT（HttpOnly 会话 cookie）。
package auth

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
)

// CookieName 会话 cookie 名。
const CookieName = "hd_session"

const ctxKey = "hd_user"

// dummyHash 用于账号不存在时做等时比较，避免用户枚举。
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("hengdesk-dummy"), bcrypt.DefaultCost)

// Service 认证服务。
type Service struct {
	DB     *ent.Client
	Audit  audit.Writer
	Secret []byte
	TTL    time.Duration
	Secure bool
}

// Principal 当前登录用户。
type Principal struct {
	ID           int      `json:"id"`
	OrgID        int      `json:"org_id"`
	Email        string   `json:"email"`
	DisplayName  string   `json:"display_name"`
	IsSuperAdmin bool     `json:"is_super_admin"`
	Roles        []string `json:"roles"`
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
}

func (s *Service) login(c echo.Context) error {
	var req loginReq
	if err := c.Bind(&req); err != nil || req.Email == "" || req.Password == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "请输入邮箱和密码")
	}
	ctx := c.Request().Context()
	email := strings.ToLower(strings.TrimSpace(req.Email))
	u, err := s.DB.User.Query().Where(user.Email(email)).Only(ctx)
	ok := false
	if err == nil {
		ok = u.PasswordHash != "" && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) == nil && u.Status == user.StatusActive
	} else {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
	}
	e := audit.Entry{Action: "auth.login", ActorEmail: email, IP: c.RealIP(), UserAgent: c.Request().UserAgent(),
		RequestID: c.Response().Header().Get(echo.HeaderXRequestID), Outcome: "denied"}
	if ok {
		e.Outcome, e.ActorID, e.OrgID = "success", u.ID, u.OrgID
	}
	if aerr := s.Audit.Write(ctx, e); aerr != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "审计服务不可用，操作已拒绝")
	}
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "邮箱或密码错误")
	}
	now := time.Now()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject: strconv.Itoa(u.ID), IssuedAt: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(s.TTL)), Issuer: "hengdesk",
	}).SignedString(s.Secret)
	if err != nil {
		return err
	}
	_ = s.DB.User.UpdateOne(u).SetLastLoginAt(now).Exec(ctx)
	c.SetCookie(&http.Cookie{Name: CookieName, Value: tok, Path: "/", HttpOnly: true, Secure: s.Secure,
		SameSite: http.SameSiteLaxMode, Expires: now.Add(s.TTL)})
	p, err := s.principal(c, u.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

func (s *Service) logout(c echo.Context) error {
	c.SetCookie(&http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: s.Secure, MaxAge: -1})
	return c.NoContent(http.StatusNoContent)
}

func (s *Service) me(c echo.Context) error { return c.JSON(http.StatusOK, Current(c)) }

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

// Parse 校验 token 返回用户 ID。
func (s *Service) Parse(tok string) (int, error) {
	t, err := jwt.ParseWithClaims(tok, &jwt.RegisteredClaims{}, func(*jwt.Token) (any, error) { return s.Secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("hengdesk"))
	if err != nil {
		return 0, err
	}
	sub, _ := t.Claims.GetSubject()
	id, err := strconv.Atoi(sub)
	if err != nil {
		return 0, errors.New("bad subject")
	}
	return id, nil
}

// Optional 若带有效会话则注入 Principal，不强制。
func (s *Service) Optional(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if ck, err := c.Cookie(CookieName); err == nil && ck.Value != "" {
			if id, err := s.Parse(ck.Value); err == nil {
				if p, err := s.principal(c, id); err == nil {
					c.Set(ctxKey, p)
				}
			}
		}
		return next(c)
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

// Actor 供审计中间件使用。
func Actor(c echo.Context) (int, int, string) {
	if p := Current(c); p != nil {
		return p.OrgID, p.ID, p.Email
	}
	return 0, 0, ""
}
