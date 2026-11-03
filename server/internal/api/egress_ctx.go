package api

import (
	"context"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/egress"
)

// egressCtx 把当前用户与模块写入出站上下文，用于出站日志与拒绝审计。
func egressCtx(ctx context.Context, c echo.Context, module string) context.Context {
	cl := egress.Caller{Module: module}
	if p := auth.Current(c); p != nil {
		cl.UserID, cl.Email = p.ID, p.Email
	}
	return egress.WithCaller(ctx, cl)
}
