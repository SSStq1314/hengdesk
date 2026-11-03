# D-41 对话 PR3 口径：trace_id、清理单位、法律保全、token 估算（产品经理定）

> 同步自 `decisions.md` 第 384 行起（产品经理，2026-11-03，针对 PR #36）。下面是原文，未作改写；实现说明在后面。

1. **trace_id**：仍以服务端生成的 trace_id 为准，审计主键不变。只有来自「受信代理 IP 列表」（可配置，默认为空）的请求，才接受上游网关传来的 traceparent，并单独存为 upstream_trace_id，仅用于关联排查。其他来源传来的一律忽略。
2. **清理单位**：同意以整个会话为单位，按最后活动时间判断是否到期。活跃会话里单独删除的文件跟着会话一起清理；删除后对用户立即不可见。处于法律保全的会话不清理。
3. **法律保全的管理**：属于合规职责。
   - **审计员**可以设置和解除法律保全。
   - **超级管理员**只能设置，不能解除，防止管理员在清理前先解除保全。
   - 设置和解除都记审计；由超级管理员设置的，同时通知审计员。
4. **token 估算**：M1 够用，但只用于预检和上下文截断，截断时额外预留 10% 余量。用量记账以上游返回的 usage 为准（D-37）；只有上游没返回 usage 时才用估算值，并标为「估算」。各家的精确 tokenizer 列为 P2。

## 实现说明（工程）

| 条目 | 实现 | 测试 |
|---|---|---|
| 1. trace_id | `audit.Trace(trusted)` 中间件始终在服务端生成 trace_id（审计主键与 `X-Trace-Id` 不变）。新增配置 `HENGDESK_TRACE_TRUSTED_PROXIES`（逗号分隔的 IP / CIDR，**默认为空**，即一律不接受）。只有 **TCP 对端**在列表内时才解析 W3C `traceparent`（版本 `ff`、全零 id、格式错误一律忽略），其 trace-id 写入审计新列 `upstream_trace_id`（迁移 `20261116000000_m1_chat_pr3.sql`），仅作关联；不读 `X-Forwarded-For` 判断受信，防止客户端伪造。`upstream_trace_id` 非空时纳入审计哈希（为空时哈希格式与旧记录一致，旧链与归档包仍可校验）。 | `audit/trace_d41_test.go`：traceparent 解析用例；默认空列表忽略、非受信来源忽略（含伪造 XFF）、受信来源单独存储且 trace_id 仍为服务端生成、非法值忽略；写库后链校验通过、upstream 纳入哈希 |
| 2. 清理单位 | 未改动：以会话为单位、按最后活动时间；活跃会话中已删除的文件随会话清理；处于法律保全（按用户或全组织）的会话跳过。 | `chatretention/retention_test.go`（已有：按用户 / 全组织保全跳过、解除后清理） |
| 3. 法律保全 | 路由：查看与设置 `audit.read` 或 `system.settings`（`authz.RequireAny`），**解除仅 `audit.read`**；超级管理员解除返回 404（越权统一 404）。设置、解除都写 attempt（fail-closed）/ result 审计，设置审计带 `by_auditor`。超级管理员设置时，先通过既有高风险站内通知（`alert.high_risk`，kind=`legal_hold_set_by_admin`，带范围、成员邮箱、原因）通知审计员，通知写失败则拒绝设置。Web：保留期设置区只对超级管理员显示，保全列表对两者显示，「解除」按钮只对审计员显示；侧边栏「保留期与法律保全」对两种角色显示；审计员的高风险提醒显示保全通知。 | `api/chatpr3_test.go`：`TestLegalHoldPermissionsD41`（审计员设置不告警、超管设置产生告警、超管解除 404 且未解除、审计员解除、设置 / 解除审计、成员 404）；`TestChatRetentionAdminAndLegalHold` 相应调整 |
| 4. token 估算 | 估算只用于预检与截断：截断预算 = 上下文窗口 − 输出预留 − **窗口的 10%**。记账沿用 D-37：以上游 usage 为准，上游未返回时用估算并标记；消息新增 `usage_estimated` 列并在接口返回，界面在回复下标「用量为估算」。各家精确 tokenizer 列为 P2。 | `api/contexttrunc_test.go`（10% 余量）；`api/chatpr3_test.go`：`TestMessageUsageEstimatedD41`（有 usage 不标记、无 usage 标「估算」）；`models_ext_test.go`（已有：用量记录 estimated） |
