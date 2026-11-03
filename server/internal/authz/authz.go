// Package authz 授权辅助。硬规则：越权一律返回 404，不暴露资源是否存在。
package authz

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
)

// ErrNotFound 统一的越权/不存在响应。
var ErrNotFound = echo.NewHTTPError(http.StatusNotFound, "资源不存在")

// RequireRole 未登录 401；已登录但无角色（超管视为拥有全部管理角色）一律 404。
func RequireRole(keys ...string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			p := auth.Current(c)
			if p == nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "未登录")
			}
			if !p.IsSuperAdmin && !p.HasRole(keys...) {
				return ErrNotFound
			}
			return next(c)
		}
	}
}
