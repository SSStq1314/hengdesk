// Package rbac 定义内置角色、权限点与角色-权限矩阵（PRD FR-06，三员分立）。
//
// 这是叶子包，不依赖 HTTP 或数据库，auth / authz / store 都从这里取定义。
// 内置角色的权限以代码为准；数据库 roles.permissions 只是同步展示用的副本。
package rbac

import "sort"

// 内置角色键。
const (
	SuperAdmin      = "super_admin"
	SecurityAuditor = "security_auditor"
	OrgAdmin        = "org_admin"
	DeptAdmin       = "dept_admin"
	Member          = "member"
)

// Perm 权限点。
type Perm string

// 权限点清单。新增权限点时同步更新 Matrix 与前端 locales 中的说明。
const (
	MemberRead      Perm = "org.member.read"      // 查看成员
	MemberManage    Perm = "org.member.manage"    // 新增 / 编辑 / 停用成员、重置密码、强制下线、解锁
	DeptRead        Perm = "org.dept.read"        // 查看部门树
	DeptManage      Perm = "org.dept.manage"      // 部门增删改、CSV 导入
	RoleRead        Perm = "org.role.read"        // 查看角色矩阵
	RoleAssign      Perm = "org.role.assign"      // 分配普通角色（组织管理员 / 部门管理员 / 成员）
	RoleAssignPriv  Perm = "org.role.assign_priv" // 分配特权角色（超级管理员 / 安全审计员）
	SSOManage       Perm = "auth.sso.manage"      // 认证设置（OIDC）
	SettingsManage  Perm = "system.settings"      // 系统设置
	ToolManage      Perm = "tool.manage"          // 工具注册与启用（新工具默认禁用）
	ModelManage     Perm = "model.manage"         // 模型供应商与模型管理（含上游密钥，只写不读）
	AuditRead       Perm = "audit.read"           // 查看审计日志（本身也记审计）
	AuditExport     Perm = "audit.export"         // 导出审计日志
	KBCreate        Perm = "kb.create"            // 创建知识库
	AgentCreate     Perm = "agent.create"         // 创建智能体
	ResourceACLRead Perm = "acl.read"             // 查看资源授权（具体资源还需 ACL manage 位）
)

// Role 内置角色定义。
type Role struct {
	Key   string
	Name  string
	Perms []Perm
	// DeptScoped 为 true 时，该角色的成员 / 部门类权限只作用于所在部门及其下级。
	DeptScoped bool
	// Privileged 特权角色，只有持有 RoleAssignPriv 的人能授予或撤销。
	Privileged bool
}

// Builtin 5 个内置角色，顺序即展示顺序。
//
// 三员分立：
//   - 超级管理员管系统配置和人，**不能**查看审计日志；
//   - 安全审计员只读，能看全部审计和组织结构，**不能**改任何配置；
//   - 任何角色都不能修改或删除审计记录（数据库触发器兜底）。
var Builtin = []Role{
	{Key: SuperAdmin, Name: "超级管理员", Privileged: true, Perms: []Perm{
		MemberRead, MemberManage, DeptRead, DeptManage, RoleRead, RoleAssign, RoleAssignPriv,
		SSOManage, SettingsManage, ToolManage, ModelManage, KBCreate, AgentCreate, ResourceACLRead,
	}},
	{Key: SecurityAuditor, Name: "安全审计员", Privileged: true, Perms: []Perm{
		AuditRead, AuditExport, MemberRead, DeptRead, RoleRead,
	}},
	{Key: OrgAdmin, Name: "组织管理员", Perms: []Perm{
		MemberRead, MemberManage, DeptRead, DeptManage, RoleRead, RoleAssign,
		ToolManage, KBCreate, AgentCreate, ResourceACLRead,
	}},
	{Key: DeptAdmin, Name: "部门管理员", DeptScoped: true, Perms: []Perm{
		MemberRead, DeptRead, RoleRead, KBCreate, AgentCreate,
	}},
	{Key: Member, Name: "成员", Perms: []Perm{}},
}

// Lookup 取内置角色。
func Lookup(key string) (Role, bool) {
	for _, r := range Builtin {
		if r.Key == key {
			return r, true
		}
	}
	return Role{}, false
}

// AllPerms 全部权限点（排序）。
func AllPerms() []Perm {
	seen := map[Perm]bool{}
	for _, r := range Builtin {
		for _, p := range r.Perms {
			seen[p] = true
		}
	}
	out := make([]Perm, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Exclusive 互斥的角色对（三员分立）：同一人不能同时持有。
var Exclusive = [][2]string{{SuperAdmin, SecurityAuditor}}

// Conflicts 若 have 中已有与 add 互斥的角色，返回该角色键。
func Conflicts(have []string, add string) (string, bool) {
	for _, pair := range Exclusive {
		for i := 0; i < 2; i++ {
			if pair[i] == add {
				other := pair[1-i]
				for _, h := range have {
					if h == other {
						return other, true
					}
				}
			}
		}
	}
	return "", false
}

// Grant 一条角色授予：Dept 非 0 表示部门范围（仅 DeptScoped 角色有意义）。
type Grant struct {
	Role string
	Dept int
}

// Set 某用户的有效权限集合。
type Set struct {
	org  map[Perm]bool         // 组织范围生效
	dept map[Perm]map[int]bool // 仅在指定部门（含下级）生效
}

// Resolve 由角色授予计算权限集合。未知角色忽略（fail-closed）。
func Resolve(grants []Grant) Set {
	s := Set{org: map[Perm]bool{}, dept: map[Perm]map[int]bool{}}
	for _, g := range grants {
		r, ok := Lookup(g.Role)
		if !ok {
			continue
		}
		for _, p := range r.Perms {
			if r.DeptScoped {
				if g.Dept == 0 {
					continue // 部门管理员必须绑定部门，否则不给权限
				}
				if s.dept[p] == nil {
					s.dept[p] = map[int]bool{}
				}
				s.dept[p][g.Dept] = true
			} else {
				s.org[p] = true
			}
		}
	}
	return s
}

// Has 组织范围是否拥有权限。
func (s Set) Has(p Perm) bool { return s.org[p] }

// HasAny 组织范围或任一部门范围拥有权限（用于判断能否进入页面）。
func (s Set) HasAny(p Perm) bool { return s.org[p] || len(s.dept[p]) > 0 }

// DeptRoots 权限 p 仅在这些部门（含下级）生效；组织范围拥有时返回 nil, true。
func (s Set) DeptRoots(p Perm) (roots []int, orgWide bool) {
	if s.org[p] {
		return nil, true
	}
	for d := range s.dept[p] {
		roots = append(roots, d)
	}
	sort.Ints(roots)
	return roots, false
}

// List 拥有的权限点（组织或部门范围），供前端决定菜单。
func (s Set) List() []string {
	out := []string{}
	for _, p := range AllPerms() {
		if s.HasAny(p) {
			out = append(out, string(p))
		}
	}
	return out
}
