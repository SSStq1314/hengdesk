# 决策记录（仓库副本）

产品决策主文件在产品仓库 `ai-office-platform/decisions.md`。与工程实现直接相关的决策在此保留同步副本：

| 编号 | 标题 | 文件 |
|---|---|---|
| D-28 | 存储与缓存许可：采纳 Valkey，默认 SeaweedFS，MinIO 降为可选 | [D-28-storage-cache-license.md](D-28-storage-cache-license.md) |
| D-29 | Go 工具链改为 1.26 与依赖安全门禁（取代 D-24 中 Go 1.25） | [D-29-go-toolchain-govulncheck.md](D-29-go-toolchain-govulncheck.md) |
| D-30 | M1 工程默认项确认（产品经理） | [D-30-m1-engineering-defaults.md](D-30-m1-engineering-defaults.md) |
| D-31 | 数据库迁移：ent + Atlas 版本化、只向前、升级前自动备份 | [D-31-versioned-migrations-backup.md](D-31-versioned-migrations-backup.md) |
| D-32 | 统一授权层：RBAC 五角色、三员分立、资源 ACL | [D-32-authz-rbac-acl.md](D-32-authz-rbac-acl.md) |
| D-33 | 会话、锁定、密码与角色默认值确认（补充 D-30 / D-32） | [D-33-session-lockout-password-defaults.md](D-33-session-lockout-password-defaults.md) |

> D-27（ACL 检索分档策略与性能基线）由产品经理维护，不在此复制。
