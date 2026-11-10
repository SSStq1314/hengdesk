# 审计

- 只追加：ent Hook 与 PostgreSQL 触发器双重拒绝 UPDATE / DELETE / TRUNCATE。
- 哈希链：`hash = sha256(prev_hash ‖ 规范化 JSON)`，`prev_hash` 唯一索引（仅已入链行）保证链不分叉。PostgreSQL 上写入在同一事务内先取事务级 advisory lock（`pg_advisory_xact_lock`，固定键），再读链尾、算哈希、插入；拿锁或写入失败仍按 fail-closed 拒绝。SQLite 单测路径保持进程内互斥 + 乐观重试。M1 之前的旧行 `hash` 为空，校验时计为 legacy。
- detail 规范化（D5）：入链前将 `detail` 做 JSON marshal → unmarshal，再以往返后的形态计算哈希并落库。调用方若把 struct（例如 `model.call` 的 `gateway.Usage`）放进 `map[string]any`，直接序列化会按字段声明顺序，而 PostgreSQL jsonb 读回后键按字母序，`Verify` 重算哈希会对不上。规范化在 `EntWriter.Write` 内统一完成，覆盖全部写审计调用点。
  - 兼容（D-48）：不为旧哈希做迁移。含 struct / 具名类型的 detail 在 #68 修复前写入的哈希与新算法不同，`Verify` 会在第一条这类记录处报断裂；仅含 map / 标量的记录哈希不变。没有数据迁移脚本。演示库和测试库须清空重建（或丢弃后用修复后的程序重新生成归档包）。修复前已签名的归档包需用当时的程序校验。
- 哈希方案版本（D-48）：自第一个正式版起，**不得静默改变** `canonical` / `NormalizeDetail` 等入链规范化方式。当前基线是 **方案版本 1**，以 main `bf4d437`（PR #68，D5 detail 规范化 + 跨进程 advisory lock）为准：时间截断到微秒并转 UTC，`detail` 先 JSON 往返再入链，`upstream_trace_id` 为空时不参与哈希。这一版尚未把版本号写入哈希字节（避免再次改变已公布的算法）。此后任何改动必须：
  1. 显式提升哈希方案版本号（例如在记录或规范化载荷中增加 `hash_version`）；
  2. `Verify` 按版本选用对应算法，或提供迁移；
  3. 在本文件与发布说明中写明兼容策略。
  未升版本就改变规范化，视为破坏审计链的缺陷。
- fail-closed：变更请求先写 attempt，失败 503 且不执行；越权拒绝（404）与登录失败同步写审计，失败同样 503，响应不暴露资源是否存在。登录失败只记账号，绝不记密码。
- 敏感读取才审计 GET：查看审计（audit.view）、校验链（audit.verify）、导出（audit.export），后续加下载原件。
- 接口（需 `audit.read`；导出需 `audit.export`）：
  - `GET /api/v1/admin/audit-logs?actor=&action=&outcome=&from=&to=&before_id=&limit=`
  - `GET /api/v1/admin/audit-logs/verify`
  - `GET /api/v1/admin/audit-logs/export`（CSV，UTF-8 BOM，最多 1 万行，防公式注入）
  - `GET /api/v1/admin/audit-logs/retention`（保留期状态与最近归档断点，FR-40）
- 前端：`/audit`，仅持有 `audit.read` 的用户可见。
- 保留期与归档（FR-40）：默认 365 天、最低 180 天，到期前 30 天提醒审计员，只能由审计员手动归档；删除前先在链上写归档断点，触发器只放行检查点范围内的 DELETE，校验时剩余链由断点锚定。运维说明见 [docs/ops/audit-retention.md](../ops/audit-retention.md)。
