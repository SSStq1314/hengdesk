package api

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/pkg/edition"
)

// host 把社区版能力以稳定接口提供给专业版扩展。
type host struct{ a *auth.Service }

func (h host) RequireUser() echo.MiddlewareFunc { return h.a.RequireUser }

func (h host) RequirePerm(p string) echo.MiddlewareFunc { return authz.Require(rbac.Perm(p)) }

func (h host) Audit(c echo.Context, action, rt, rid string, detail map[string]any) error {
	return audit.Record(c, audit.Entry{Action: action, ResourceType: rt, ResourceID: rid, Outcome: "success", Detail: detail})
}

func (h host) CurrentUser(c echo.Context) (int, int) {
	if p := auth.Current(c); p != nil {
		return p.ID, p.OrgID
	}
	return 0, 0
}

// mountEdition 挂载 GET /edition 与已注册的专业版扩展。
func mountEdition(g *echo.Group, a *auth.Service) {
	g.GET("/edition", func(c echo.Context) error { return c.JSON(http.StatusOK, edition.Current()) }, a.RequireUser)
	for _, ext := range edition.Extensions() {
		if err := ext.Mount(g.Group("/pro/"+ext.Name()), host{a: a}); err != nil {
			panic("edition: 扩展 " + ext.Name() + " 挂载失败: " + err.Error())
		}
	}
}
