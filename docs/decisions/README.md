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
| D-40 | 对话：M1 只软删除、按人物理删除列 P1、保留期默认不清理（开启后 365 天 / 最低 180 天）、token 截断保留系统提示词与最新用户消息、「已停止」、只对最后一条回复「重新生成」/「重试」、同一会话同时只生成一条（409）、记住的模型失效回退默认模型 | [D-40-chat-rules.md](D-40-chat-rules.md) |
| D-41 | 对话 PR3 口径：trace_id 以服务端为准、仅受信代理的 traceparent 存 upstream_trace_id（默认不接受）；按会话与最后活动时间清理、保全会话不清理；法律保全审计员可设置和解除、超管只能设置（解除 404）且通知审计员；token 估算只用于预检和截断（额外 10% 余量），记账以上游 usage 为准、缺失时标「估算」，精确 tokenizer 列 P2 | [D-41-chat-pr3.md](D-41-chat-pr3.md) |
| D-42 | 知识库检索性能定稿：按硬件档与可见比例定目标（推荐 32 GB + 预热）、ef_search 400、halfvec 索引、启动预热、索引超内存一半提示、按知识库过滤列 P1；超管给自己授权记高风险审计、embedding 计入用量与告警；检索失败 503 + 重试 / 不用知识库继续、未检索到资料标「未引用知识库资料」、按知识库「仅依据资料回答」；改写追问 P1、多层代理 P2 | [D-42-kb-perf-defaults.md](D-42-kb-perf-defaults.md) |
| D-43 | HNSW 召回参数：ef_search 默认 1000（可按可见比例自适应但不得低于各档目标）、100% 可见档纳入考核（M1 recall@10 ≥0.93、M2 ≥0.95：无过滤路径调大 ef_search 并调 m / ef_construction）、按知识库过滤 P1、M1 验收复测不强制 32 GB | [D-43-hnsw-recall.md](D-43-hnsw-recall.md) |
| D-44 | MCP 与审批默认值：高危永不自动（含超管）、通过即执行且复核发起人权限、空审批人提醒工具管理员、审批结果后 worker 自动续写（「继续」仅兜底）、等待审批锁定与撤回续写、AC-08 重启恢复测试；工具步数可配与角色授权 UI 列 P1 | [D-44-mcp-approval-defaults.md](D-44-mcp-approval-defaults.md) |
| D-45 | 智能体运行时：发布快照可回滚、运行时调用人权限交集与越权测试、步数 1–10 默认 5、可逆停用与只读会话；正式下架/上架 P1、委托授权 P2 | [D-45-agent-runtime-defaults.md](D-45-agent-runtime-defaults.md) |
| D-46 | 统计 / AIGC / 离线包：查看他人明细记审计、日界按系统时区、导出审计含筛选与行数、标识文案校验、导出 front matter、卸载确认与保留 secrets；P1/开放项不进 M1 | [D-46-stats-aigc-offline.md](D-46-stats-aigc-offline.md) |
| D-47 | 通义冒烟挂起至 M2 W1，改用 vLLM CPU 小模型协议冒烟；审计链多进程写入误拒为 M1 阻塞（#68 已合入） | 见产品库 `decisions.md` D-47；工程修复 #68 |
| D-48 | 旧审计链不迁移，演示/测试库清空重建；正式包基于 `bf4d437`+；验收用新库；哈希规范化不得静默改变，改动须带版本号 | 见产品库 `decisions.md` D-48；`docs/dev/audit.md`、`docs/release/m1.md` |
| D-49 | 通义真实冒烟移出 M1，改官方文档契约测试 + 标明来源的录制回放（禁止编造）；vLLM CPU 真实冒烟保留；POC 现场用客户密钥做通义冒烟。其余外部依赖（hengdesk-pro、License 私钥、规模测试机器、企业微信、LDAP/AD 容器）见产品库全文 | 见产品库 `decisions.md` D-49。通义回放带已换成官方示例（`dashscope_cassette.json`），不宣称真实调用。vLLM CPU 实跑与 D-50 断网安装另见后续 PR |

> D-27（ACL 检索分档策略与性能基线）由产品经理维护，不在此复制。
