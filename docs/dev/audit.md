# 审计

- 只追加：ent Hook 与 PostgreSQL 触发器双重拒绝 UPDATE / DELETE / TRUNCATE。
- 哈希链：`hash = sha256(prev_hash ‖ 规范化 JSON)`，`prev_hash` 唯一索引（仅已入链行）保证链不分叉，并发写入冲突时重读链尾重试。M1 之前的旧行 `hash` 为空，校验时计为 legacy。
- fail-closed：变更请求先写 attempt，失败 503 且不执行；越权拒绝（404）与登录失败同步写审计，失败同样 503，响应不暴露资源是否存在。登录失败只记账号，绝不记密码。
- 敏感读取才审计 GET：查看审计（audit.view）、校验链（audit.verify）、导出（audit.export），后续加下载原件。
- 接口（需 `audit.read`；导出需 `audit.export`）：
  - `GET /api/v1/admin/audit-logs?actor=&action=&outcome=&from=&to=&before_id=&limit=`
  - `GET /api/v1/admin/audit-logs/verify`
  - `GET /api/v1/admin/audit-logs/export`（CSV，UTF-8 BOM，最多 1 万行，防公式注入）
  - `GET /api/v1/admin/audit-logs/retention`（保留期状态与最近归档断点，FR-40）
- 前端：`/audit`，仅持有 `audit.read` 的用户可见。
- 保留期与归档（FR-40）：默认 365 天、最低 180 天，到期前 30 天提醒审计员，只能由审计员手动归档；删除前先在链上写归档断点，触发器只放行检查点范围内的 DELETE，校验时剩余链由断点锚定。运维说明见 [docs/ops/audit-retention.md](../ops/audit-retention.md)。
