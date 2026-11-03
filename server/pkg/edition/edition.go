// Package edition 定义社区版与专业版之间的扩展点（D-30：社区版 Apache-2.0，专业版为独立闭源模块）。
//
// 规则：
//   - 社区版只定义接口与注册表，不 import 任何专业版路径（CI 检查 scripts/check-edition-imports.sh）。
//   - 专业版在独立 module（github.com/SSStq1314/hengdesk-pro）中实现 Extension，
//     在 init 里调用 Register，并用自己的 main 调用 cli.Main。
//   - 未注册任何扩展时即为社区版，行为与功能完全不受影响。
package edition

import (
	"fmt"
	"sort"
	"sync"

	"github.com/labstack/echo/v4"
)

// Host 是社区版提供给扩展的能力（只暴露稳定接口，不暴露内部类型）。
type Host interface {
	// RequireUser 要求已登录（未登录 401）。
	RequireUser() echo.MiddlewareFunc
	// RequirePerm 要求已登录且持有权限点（无权限 404）。
	RequirePerm(perm string) echo.MiddlewareFunc
	// Audit 同步写一条审计，失败时返回应直接回给客户端的错误（fail-closed）。
	Audit(c echo.Context, action, resourceType, resourceID string, detail map[string]any) error
	// CurrentUser 当前登录用户 ID 与组织 ID（未登录为 0）。
	CurrentUser(c echo.Context) (userID, orgID int)
}

// Extension 专业版扩展。
type Extension interface {
	// Name 扩展名（唯一）。
	Name() string
	// Features 扩展提供的功能标识，前端据此显示入口。
	Features() []string
	// Mount 在 /api/v1/pro/<Name> 下挂载路由；路由已经过会话、审计与 CSRF 中间件。
	Mount(g *echo.Group, h Host) error
}

var (
	mu   sync.RWMutex
	exts = map[string]Extension{}
)

// Register 注册扩展；重名 panic（启动期发现配置错误）。
func Register(e Extension) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := exts[e.Name()]; dup {
		panic(fmt.Sprintf("edition: 扩展 %q 重复注册", e.Name()))
	}
	exts[e.Name()] = e
}

// Extensions 按名称排序返回已注册扩展。
func Extensions() []Extension {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Extension, 0, len(exts))
	for _, e := range exts {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Info 版本信息。
type Info struct {
	Edition  string   `json:"edition"` // community / professional
	Features []string `json:"features"`
}

// Current 当前版本信息。
func Current() Info {
	in := Info{Edition: "community", Features: []string{}}
	for _, e := range Extensions() {
		in.Edition = "professional"
		in.Features = append(in.Features, e.Features()...)
	}
	sort.Strings(in.Features)
	return in
}

// ResetForTest 清空注册表，仅测试使用。
func ResetForTest() {
	mu.Lock()
	exts = map[string]Extension{}
	mu.Unlock()
}
