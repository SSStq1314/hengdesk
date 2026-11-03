# 社区版与专业版扩展点（D-30）

- 社区版：本仓库，Apache-2.0。
- 专业版：独立闭源 module `github.com/SSStq1314/hengdesk-pro`，单向依赖社区版。

## 扩展点
`server/pkg/edition`（公开包，专业版 module 可 import）：
- `Extension`：`Name()`、`Features()`、`Mount(g *echo.Group, h Host)`。路由挂在 `/api/v1/pro/<Name>`，已经过会话、审计（变更类请求 fail-closed）与 CSRF 中间件。
- `Host`：社区版提供的稳定能力：`RequireUser()`（未登录 401）、`RequirePerm(perm)`（无权限 404）、`Audit(...)`（fail-closed）、`CurrentUser(c)`。扩展**不能**访问 `internal/` 包。
- `Register(ext)`：在扩展包的 `init` 中调用；`GET /api/v1/edition`（需登录）返回 `{edition, features}`，前端据此显示专业版入口。

`server/pkg/cli.Main()` 是命令行实现，社区版入口 `server/cmd/hengdesk` 只调用它。

## 专业版如何注入
```go
// hengdesk-pro/cmd/hengdesk-pro/main.go（闭源仓库）
package main

import (
	_ "github.com/SSStq1314/hengdesk-pro/sso" // init 中 edition.Register(...)
	"github.com/SSStq1314/hengdesk/server/pkg/cli"
)

func main() { cli.Main() }
```
专业版镜像用自己的 Dockerfile 构建该 main；社区版构建产物中不含任何专业版代码。选择独立 module 而非 build tag，是因为 `go mod tidy` 会解析所有 build tag 下的 import，build tag 方案会让社区版 go.mod 出现专业版依赖。

## CI 检查
`scripts/check-edition-imports.sh`（go job 中执行）：
1. 任何 Go 文件（含所有 build tag）、go.mod / go.work、前端源码不得引用 `github.com/SSStq1314/hengdesk-pro`；
2. 仓库内不得出现 `server/pro` 等专业版目录；
3. `go list -deps -test ./...` 依赖图不含专业版包。

## 新工具默认禁用
扩展注册的工具同样走 `authz.ToolUsable`，默认禁用（D-32）。
