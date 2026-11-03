package auth

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
)

// adminBindOIDC PUT /admin/users/:id/oidc-identity {issuer?, subject}
// 管理员手动把账号绑定到 OIDC 身份（D-34：特权账号只能这样绑定）。目标为特权账号时由 Target 校验 RoleAssignPriv。
func (s *Service) adminBindOIDC(c echo.Context) error {
	u, err := s.target(c)
	if err != nil {
		return err
	}
	var req struct {
		Issuer  string `json:"issuer"`
		Subject string `json:"subject"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	req.Issuer, req.Subject = strings.TrimSpace(req.Issuer), strings.TrimSpace(req.Subject)
	if req.Issuer == "" && s.OIDC != nil {
		req.Issuer = s.OIDC.Cfg.Issuer
	}
	if req.Issuer == "" || req.Subject == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "请填写 OIDC 身份标识（subject）")
	}
	ctx := c.Request().Context()
	taken, err := s.DB.User.Query().Where(user.OidcIssuer(req.Issuer), user.OidcSubject(req.Subject), user.IDNEQ(u.ID)).Exist(ctx)
	if err != nil {
		return err
	}
	if taken {
		return echo.NewHTTPError(http.StatusConflict, "该 OIDC 身份已绑定其他账号")
	}
	priv, err := IsPrivileged(ctx, s.DB, u.ID)
	if err != nil {
		return err
	}
	if err := s.writeBindAudit(c, u, "auth.oidc.bind", map[string]any{"issuer": req.Issuer, "subject": req.Subject,
		"previous_subject": u.OidcSubject, "privileged": priv}); err != nil {
		return err
	}
	if err := s.DB.User.UpdateOne(u).SetOidcIssuer(req.Issuer).SetOidcSubject(req.Subject).Exec(ctx); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// adminUnbindOIDC DELETE /admin/users/:id/oidc-identity
func (s *Service) adminUnbindOIDC(c echo.Context) error {
	u, err := s.target(c)
	if err != nil {
		return err
	}
	if u.OidcSubject == "" {
		return c.NoContent(http.StatusNoContent)
	}
	if err := s.writeBindAudit(c, u, "auth.oidc.unbind", map[string]any{"issuer": u.OidcIssuer, "subject": u.OidcSubject}); err != nil {
		return err
	}
	if err := s.DB.User.UpdateOne(u).SetOidcIssuer("").SetOidcSubject("").Exec(c.Request().Context()); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Service) writeBindAudit(c echo.Context, u *ent.User, action string, detail map[string]any) error {
	org, actorID, actorEmail := Actor(c)
	e := s.entry(c, action, actorEmail)
	e.Outcome, e.OrgID, e.ActorID = "success", org, actorID
	e.ResourceType, e.ResourceID, e.Detail = "user", u.Email, detail
	if err := s.Audit.Write(c.Request().Context(), e); err != nil {
		return audit.ErrRejected
	}
	return nil
}
