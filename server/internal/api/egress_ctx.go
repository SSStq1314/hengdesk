package api

import (
	"context"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/egress"
)

// egressCtx 把当前用户与模块写入出站上下文，用于出站日志与拒绝审计。
//
// 默认不声明「审计已 fail-closed」：放行日志写不进去时出站被拒绝（D-38 第 9 条）。
func egressCtx(ctx context.Context, c echo.Context, module string) context.Context {
	return withEgressCaller(ctx, c, egress.Caller{Module: module})
}

// egressCtxAudited 仅供已在出站前按 fail-closed 写好审计的调用方使用（目前只有模型网关的对话请求，
// 见 chat.go 的 model.chat attempt 审计）。新增调用方必须先满足这一点，再改用本函数。
func egressCtxAudited(ctx context.Context, c echo.Context, module string) context.Context {
	return withEgressCaller(ctx, c, egress.Caller{Module: module, AuditedFailClosed: true})
}

func withEgressCaller(ctx context.Context, c echo.Context, cl egress.Caller) context.Context {
	if p := auth.Current(c); p != nil {
		cl.UserID, cl.Email = p.ID, p.Email
	}
	return egress.WithCaller(ctx, cl)
}
