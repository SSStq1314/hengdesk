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
| D-34 | M1 第二批默认值：OIDC 建号 / 绑定 / 强制 SSO，端口、自签证书、审计保留期 | [D-34-m1-batch2-defaults.md](D-34-m1-batch2-defaults.md) |
| D-35 | 模型网关客户端：标准库自研，每家上游录制回放契约测试，通义 / vLLM 冒烟列入 M1 验收 | [D-35-model-gateway-client.md](D-35-model-gateway-client.md) |
| D-36 | 主密钥：安装确认与后台提示、事务内轮换、启动自检、备份不含主密钥 | [D-36-master-key.md](D-36-master-key.md) |
| D-37 | 预算与配额默认规则：测试连接不拦截、用量未知标记、系统时区、告警状态入库、组织管理员可管理 / 部门管理员只读 | [D-37-budget-defaults.md](D-37-budget-defaults.md) |
| D-38 | 出站控制默认规则：自动条目复核、空端口仅 443、基础设施豁免限声明地址、放行日志失败按调用方声明处理、出站日志读取留痕 | [D-38-egress-defaults.md](D-38-egress-defaults.md) |
| D-39 | 备用模型按调用人权限切换、切换记录实际模型、读超时不重试、首 token 后不切换、`*` 端口收窄与被拦截列表、导出 10 万行流式、命令行工具写出站日志 | [D-39-fallback-egress-followups.md](D-39-fallback-egress-followups.md) |

> D-27（ACL 检索分档策略与性能基线）由产品经理维护，不在此复制。
