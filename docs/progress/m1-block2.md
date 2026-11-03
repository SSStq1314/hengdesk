# M1 第二块：模型网关、预算与配额、出站控制

## PR 拆分

| PR | 内容 | 状态 |
|----|------|------|
| PR1 模型网关核心 | OpenAI 兼容适配器（流式 / 非流式、tools、function calling）；DeepSeek 方言（思考模式 tool_choice 降级或拒绝、末块 usage、reasoning_content 回传、401 脱敏）；模型与上游 Admin CRUD（AES-256-GCM 加密 Key，主密钥来自安装时生成的文件 / 环境变量，API 永不返回明文）；每次调用 fail-closed 审计；统一内部 Chat API（SSE）；模拟上游 + 测试 | PR #25 已合并 |
| PR2 模型管理与用量 | 模型 ACL 管理界面、默认模型、价格表、usage_record 落库与查询、“测试连接”按钮、Admin 前端页面；D-36 主密钥；D-35 DeepSeek 契约测试 | PR #26 已合并 |
| PR3 预算与配额 | 组织 / 部门 / 用户预算，Valkey 计数、预检拦截、结束后按实际用量记账、阈值站内提醒、对账重建、管理页 | PR #27 已合并 |
| PR4 出站控制 | 出站域名白名单、出站日志（AC-09）、网关 HTTP 客户端统一走出站控制 | 本 PR |
| PR5 适配器扩展 | Anthropic 适配器、Ollama / 本地模型方言、重试与故障转移 | 待做 |

## PR1 说明

- 没有采用 go-openai（偏离 D-24 原设想）：需要透传 DeepSeek 的 `reasoning_content`、`thinking` 字段，
  并自行控制 SSE 解析、超时与错误脱敏，因此用标准库实现了一个精简客户端（`server/internal/gateway`），
  未复制任何第三方代码。
- 模型使用权限：超级管理员（`model.manage`）可用全部模型；其他用户需在 ACL 中对 `model/<模型名>` 拥有 use 位。
  无权限、模型不存在或已停用一律 404。管理界面放到 PR2。
- 审计：调用上游前先写 `model.call` 尝试记录，写失败则拒绝调用（503，不触达上游）；
  调用结束写结果记录（耗时、usage、错误码、warning），只记录元数据，不记录对话内容。
- 详细用法见 `docs/ops/model-gateway.md`。

## PR1 验证

- CI：仅使用模拟上游（`gateway/mockupstream`，复现 DeepSeek 行为）。
- 本机真实 DeepSeek 冒烟（deepseek-flash，思考模式）：**通过**。流式 + 工具调用（required 被降级为 auto 并给出 warning），
  返回 1 个工具调用、带 reasoning_content、usage 来自上游；回传 reasoning_content 的第二轮正常结束；错误 Key 映射为
  `upstream_auth_failed` 且消息已脱敏。
- Ollama：本机未安装，未实测，步骤见 `docs/ops/model-gateway.md`。

## PR2 说明

- 管理后台新增「模型网关」页（需 `model.manage`，其他人接口 404、菜单不显示）：
  - 供应商：增删改，API Key 只写，「测试连接」请求上游 `/models`。
  - 模型：增删改、价格表（元 / 百万 token，库内存微元整数）、设为默认（未指定 model 的调用使用默认模型）、
    「测试连接」（发一条极短对话，用量记为 purpose=test）、使用权限（按成员 / 部门授权，部门可含下级，撤销即时生效）。
  - 用量：按模型 / 人 / 部门汇总调用次数、token、金额，可按日期过滤；最近调用明细。
- usage_record：每次调用（含失败）落一条，记录用户、调用时所在部门、模型、token（输入 / 缓存命中 / 输出 / 思考）、
  金额（按调用时价格算好落库，改价不影响历史）、耗时、错误码。上游没返回 usage 时由网关估算并标 `estimated=true`，
  汇总里单独给出估算的调用数、token 与金额，前端用「估算」标签标注。PR3 的预算与配额直接按此表的人 / 部门 / 模型维度聚合。
- 所有管理写操作 fail-closed 审计（写失败则拒绝执行）。
- D-36 主密钥：安装确认、后台常驻提示与确认、`hengdesk keys rotate-master` 事务内轮换、启动自检、备份不含主密钥，
  详见 [D-36](../decisions/D-36-master-key.md) 与 [model-gateway.md](../ops/model-gateway.md)。
- D-35：DeepSeek 录制回放契约测试（流式、思考、错误、超时）随 CI 运行。
  **通义 / vLLM 的契约测试移到 PR5**（适配器扩展）一起做。

## PR3 说明（预算与配额，FR-13 / AC-10 / D-23）

- 预算维度：组织、部门（含下级部门）、个人、模型；周期：每日 / 每月；指标：token 数或金额（微元）。
  同一对象、周期、指标只能有一条（重复返回 409）。多条同时适用时逐条检查，任一超额即拦截。
- 计数：Valkey 实时计数（键含预算更新时间与周期起点，TTL 15 分钟），`usage_records` 为权威数据。
  计数缺失时从 `usage_records` 汇总重建；「对账并重建计数」按钮 / `POST /api/v1/admin/budgets/reconcile`
  以库为准重置计数并返回偏差，写审计。
- 调用前预检：`/api/v1/chat/completions` 与模型「测试连接」在 ACL 之后、上游之前检查，超额返回
  HTTP 429、`error.code=budget_exceeded`，带预算对象、已用、上限和重置时间；拦截写 `model.call` denied 审计，
  审计写失败则拒绝。
- 记账：流式 / 非流式调用结束后按 usage_record 的实际用量累加。
- 告警：用量达到阈值（默认 80%）或 100% 时发站内提醒（顶部横幅，点「全部标记为已读」消失），
  同一预算、周期、档位只发一次；同时写 `budget.alert` 审计。
- 管理页「预算与配额」（`/budgets`，需 `budget.manage`，仅超级管理员；其他人接口 404、菜单不显示）。
  任何登录用户可用 `GET /api/v1/budgets/me` 查看对自己生效的预算。
- 审计：`budget.create / update / delete / reconcile` 全部 fail-closed。

### 待产品确认（PR3，均先按保守默认实现）

1. **Valkey 不可用时 fail-closed**：存在适用预算时拒绝调用（503 `budget_unavailable`）；没有任何适用预算时不受影响。
2. 新建预算默认策略为「拦截」，默认告警阈值 80%。
3. 部门预算包含所有下级部门；按调用时成员的主部门归属统计。
4. 模型「测试连接」也计入预算并会被拦截。
5. 失败调用按实际返回用量计（上游超时 / 5xx 无用量记 0）。
6. 周期边界按服务器时区（`TZ`）计算；不支持「每周」周期。
7. 预检 + 结束后记账：并发或单次大调用可能使用量略超上限，超出量记录在告警详情里；不做预扣。
8. 一次调用同时越过阈值和 100% 时只发 100% 一条提醒。
9. 预算管理权限 `budget.manage` 只授予超级管理员；部门管理员只接收本部门（及下级）预算的提醒，不能管理。
10. 提醒接收人：所有超级管理员；部门预算加该部门及上级部门的部门管理员；个人预算加本人及其主部门链上的部门管理员。
11. 修改预算会开启新计数（从库重建），可能再次触发同档位提醒。
12. 「按智能体」维度待智能体功能上线后再加。
13. 计数 TTL 15 分钟，最坏情况下偏差在 15 分钟内自动以库为准纠正。

## PR4 说明（出站控制，AC-09）

- **统一出站客户端** `internal/egress`：模型网关、OIDC 发现 / 换 token 全部经 `egress.Client.HTTP()`；忽略 `HTTP(S)_PROXY` 环境变量。
  `internal/egress/bypass_test.go` 扫描全部源码 AST，出现 `http.DefaultClient`、`http.DefaultTransport`、`http.Get/Post/Head/PostForm`、自建 `http.Client{}` / `http.Transport{}`、`net.Dial*`、`net.Dialer` 即测试失败（`internal/egress`、生成代码除外）。
- **白名单**（`egress_rule` 表）：类型 `host`（精确域名）/ `wildcard`（`*.example.com`，不含裸域）/ `ip` / `cidr`，可选端口列表（空 = 任意端口）。
  **白名单为空即全部拒绝**。新建 / 修改模型供应商只返回「是否在白名单内」的提示，绝不自动加入。
- **SSRF 防护**：先解析 DNS，任一解析结果命中内网 / 回环 / 链路本地 / 云元数据地址即拒绝（防混合记录与 DNS 重绑定），连接直接使用已检查的 IP；
  每一跳重定向重新判定，最多 5 跳；连接池中复用的连接每次请求仍按最新规则判定（规则缓存 5 秒，管理端修改后立即失效）。
  内网需要显式添加 `ip` / `cidr` 规则（记 `egress.private_allow` 审计）；云元数据地址只能用精确 `ip` 规则放行。
- **出站日志**（`egress_log` 表，触发器保证只追加）：时间、调用者（用户 + 模块）、目标（方法 / 协议 / 主机 / 端口 / 路径，不含查询串）、实际 IP、放行 / 拒绝及原因、状态码、耗时、出入字节；**不记请求体、不记密钥**。
  拒绝同时写 `egress.deny` 审计，审计写失败依旧拒绝（原因记 `audit_unavailable`）。
- **管理端**：`/egress` 页面——「出站白名单」（`egress.manage`，仅超级管理员；含 URL 测试）、「出站日志」（`egress.log.read`，超级管理员与安全审计员只读）。无权限接口一律 404。
  规则增删改审计 `egress.rule.create / update / delete`，fail-closed。
- **升级迁移**：首次启动新版本时，把已有模型供应商的上游主机（及已配置的 OIDC 签发方）写入白名单（来源「升级迁移」），每条记 `egress.rule.seed` 审计，只执行一次（`system_setting: egress.upgrade_seed`）。
  详见 `docs/ops/egress.md`。

### 待产品确认（PR4，均先按保守默认实现）

1. **升级迁移自动放行已有上游**：为避免升级后模型立即不可用，已有供应商的主机自动加入白名单（只此一次、逐条审计）；新建供应商不自动加。是否改为升级后全部拒绝、由管理员手动添加？
2. OIDC 签发方同样在升级时加入白名单（仅当已配置）；全新安装需管理员手动添加，否则 SSO 登录失败。
3. **出站日志不进哈希链**：日志表用数据库触发器保证只追加；拒绝事件同时进入带哈希链的审计日志。哈希链覆盖出站日志（FR-29）列为 P1。
4. 放行请求的日志写入失败只记服务日志，不阻断调用；拒绝请求的审计写失败则一律拒绝。
5. 忽略系统代理环境变量；暂不支持经企业代理出站。
6. 基础设施连接（PostgreSQL、Valkey、对象存储）不受白名单约束。
7. 端口留空表示任意端口。
8. 拒绝过宽网段（IPv4 宽于 /8、IPv6 宽于 /16）；云元数据地址必须用精确 IP 规则放行。
9. 网络层强制（容器 egress 防火墙 / NetworkPolicy）列为 P1，本 PR 只做应用层。
10. 查看出站日志本身不记审计。

## M1 验收清单（第二块相关）

- [ ] 通义（DashScope OpenAI 兼容）真实冒烟：流式、思考字段、工具调用、错误 Key 脱敏（D-35，未通过不算 M1 完成）
- [ ] vLLM 真实冒烟：同上（D-35，未通过不算 M1 完成）
- [x] DeepSeek 真实冒烟（PR1 已通过）
