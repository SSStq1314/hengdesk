# D-32 统一授权层：RBAC 五角色、三员分立、资源 ACL

- 状态：已采纳（工程默认，待产品经理确认项见 PR）
- 日期：2026-11-02

## 决策
- (a) 内置 5 个角色：super_admin（超级管理员）、security_auditor（安全审计员）、org_admin（组织管理员）、dept_admin（部门管理员，按部门子树生效）、member（成员）。权限点定义在 `server/internal/rbac`，服务启动时同步到 roles 表。
- (b) 三员分立：超级管理员与安全审计员互斥，不能授予同一人；超级管理员没有 audit.* 权限。
- (c) 组织内至少保留 1 名启用的超级管理员：撤销角色、停用账号都会校验，事务内锁住角色行，避免并发绕过。
- (d) 未登录 401；无权限、越权（含 IDOR、跨组织、操作特权账号）一律 404。
- (e) 资源 ACL：主体为 user / dept（可选 include_children，经 dept_closure 闭包表展开）/ role；权限位 view / use / edit / manage，高位蕴含低位。可见性用 SQL 子查询下推。
- (f) 工具可用的统一入口 `authz.ToolUsable`：新工具默认禁用，需管理员启用且调用者有 use 位。
- (g) 迁移：admin→org_admin，auditor→security_auditor，删除 approver 角色及其成员关系；is_super_admin=true 转为 super_admin 角色后再删除该列。
- 理由：权限点集中、可测试；404 不暴露资源是否存在；闭包表让子树查询保持单条 SQL。
- 备选方案：Casbin 等策略引擎（对 5 个固定角色而言过重，且策略文件不易审计），否决。
