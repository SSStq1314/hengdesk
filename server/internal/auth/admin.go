package auth

import (
	"net/http"
	"strconv"

	"github.com/SSStq1314/hengdesk/server/internal/audit"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
)

var errNotFound = echo.NewHTTPError(http.StatusNotFound, "资源不存在")

// RegisterAdmin 挂载账号安全类管理接口。调用方负责在 g 上挂好权限中间件。
//
//	POST /users/:id/sessions/revoke   强制下线
//	POST /users/:id/disable           停用（同时下线）
//	POST /users/:id/enable            启用
//	POST /users/:id/password-reset    管理员重置密码（同时下线）
//	POST /users/:id/unlock            解除登录锁定
func (s *Service) RegisterAdmin(g *echo.Group) {
	g.POST("/users/:id/sessions/revoke", s.adminRevoke)
	g.POST("/users/:id/disable", s.adminDisable)
	g.POST("/users/:id/enable", s.adminEnable)
	g.POST("/users/:id/password-reset", s.adminResetPassword)
	g.POST("/users/:id/unlock", s.adminUnlock)
}

// target 取目标用户；未注入 Target 时只允许同组织（跨组织或不存在一律 404）。
func (s *Service) target(c echo.Context) (*ent.User, error) {
	if s.Target != nil {
		return s.Target(c)
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return nil, errNotFound
	}
	p := Current(c)
	u, err := s.DB.User.Query().Where(user.ID(id), user.OrgID(p.OrgID)).Only(c.Request().Context())
	if err != nil {
		return nil, errNotFound
	}
	return u, nil
}

func (s *Service) adminRevoke(c echo.Context) error {
	u, err := s.target(c)
	if err != nil {
		return err
	}
	n, err := s.Sessions.RevokeUser(c.Request().Context(), u.ID)
	if err != nil {
		return ErrUnavailable
	}
	return c.JSON(http.StatusOK, map[string]int{"revoked": n})
}

func (s *Service) adminDisable(c echo.Context) error {
	u, err := s.target(c)
	if err != nil {
		return err
	}
	if u.ID == Current(c).ID {
		return echo.NewHTTPError(http.StatusBadRequest, "不能停用自己的账号")
	}
	if s.GuardDisable != nil {
		if err := s.GuardDisable(c, u); err != nil {
			return err
		}
	}
	if err := s.DB.User.UpdateOne(u).SetStatus(user.StatusDisabled).Exec(c.Request().Context()); err != nil {
		return err
	}
	if err := s.RevokeAll(c, u.ID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Service) adminEnable(c echo.Context) error {
	u, err := s.target(c)
	if err != nil {
		return err
	}
	if err := s.DB.User.UpdateOne(u).SetStatus(user.StatusActive).Exec(c.Request().Context()); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Service) adminResetPassword(c echo.Context) error {
	u, err := s.target(c)
	if err != nil {
		return err
	}
	var req struct {
		Password string `json:"new_password"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	if err := s.Passwords.Validate(req.Password, u.Email); err != nil {
		return err
	}
	if err := s.SetPassword(c, u.ID, req.Password); err != nil {
		return err
	}
	_ = s.Lockout.Reset(c.Request().Context(), u.Email)
	return c.NoContent(http.StatusNoContent)
}

func (s *Service) adminUnlock(c echo.Context) error {
	u, err := s.target(c)
	if err != nil {
		return err
	}
	if err := audit.Record(c, audit.Entry{Action: "auth.unlock", Outcome: "success",
		ResourceType: "user", ResourceID: strconv.Itoa(u.ID), Detail: map[string]any{"account": u.Email}}); err != nil {
		return err
	}
	if err := s.Lockout.Reset(c.Request().Context(), u.Email); err != nil {
		return ErrUnavailable
	}
	return c.NoContent(http.StatusNoContent)
}
