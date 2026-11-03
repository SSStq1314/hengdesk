# D-39 备用模型权限、切换记录、重试分类与出站收尾（产品经理定）

> 同步自 decisions.md（产品经理，2026-11-03，针对 PR #30、#31 的待确认项）。原文如下，实现说明附后。

**改动**
- 6.（安全，优先）切换只能落到调用人有使用权限的备用模型；都没有权限时返回原错误。配置界面在备用模型的使用人群与主模型不一致时给出提示。需要测试覆盖。
- 7. 用量和审计记录实际使用的模型，并注明「由 A 切换」。
- 5. 读超时不重试；连接超时、连接被拒可以重试或切换。
- 8. 流式响应收到首个 token 之后不再切换，直接返回错误；首个 token 之前的失败按非流式规则处理。
- 1. 新增迁移：#31 中把空端口改成 `*` 的手工条目改为 443（被模型上游引用的主机用其实际端口），并标记「待复核」。依据 `egress.rule.ports_migrate` 审计识别，用户之后改过的条目跳过；每条改动写一条审计。升级说明和后台都提供「被拦截的出站请求」列表。
- 2. 导出上限从 1 万行提高到 10 万行，流式输出。单独的导出权限列为 P1。
- 3. 真正发请求的命令行工具（cassette-record）必须走出站客户端并写出站日志；单元测试不受此限制。

**保持现状**
- 4. 保持现状。

## 实现说明

| 条目 | 实现 |
|---|---|
| 6 | `internal/api/chat.go` `usableFallbacks`：逐个按资源 ACL 的使用位校验备用模型（持有 `model.manage` 的调用人视为有权限）；全部不可用时返回主模型原错误，不调用备用、不记用量。模型接口返回 `fallback_acl_mismatch`（使用位 ACL 集合与主模型不同的备用模型 ID），模型编辑框在对应备用旁显示「使用人群与主模型不一致」。测试：`internal/api/d39_test.go`（流式 / 非流式、授权前后对照、提示字段）。 |
| 7 | `usage_records.fallback_from` 新列；审计 `resource_id` 与 `model` 为实际模型，`detail` 含 `fallback_from` 与 `note`「由 X 切换」；响应体 `model` 为实际模型并带 `fallback_from`。 |
| 5 | `internal/gateway/client.go`：拨号阶段失败（`net.OpError` Op=dial，含连接超时、连接被拒）→ 502 可重试 / 切换；连接建立后的超时 → 504 不重试；其它连接中断 → 不重试（保守）。测试 `retry_class_test.go`。 |
| 8 | 流式在首个内容 token 写出后不再切换，直接以 SSE error 事件结束；之前的失败按非流式规则。测试 `TestStreamNoSwitchAfterFirstToken`（mock 上游 `CutAfter`）。 |
| 1 | 启动时 `egress.MigrateD39Ports` 在 D-38 `MigratePorts` 之后执行一次（设置项 `egress.d39_ports` 防重入）：取 `egress.rule.ports_migrate` 审计中 manual、"" → "*" 的条目；条目已删、端口不再是 `*`、或之后有 `egress.rule.update` / `egress.rule.review` 审计的跳过；收窄为 443 或上游实际端口，置 `review_required`；与已有条目重复时删除 `*` 行并把已有条目标为待复核（审计 `merged_into`）。事务内每条写一条审计，审计失败则整体回滚。手工条目的「待复核」须管理员点「已复核」清除，编辑不清除。新接口 `GET /admin/egress/blocked?days=7`（`egress.log.read`，读取记审计，审计失败不返回数据），后台新增「被拦截的出站请求」页签，可二次确认后加入白名单。 |
| 2 | 导出上限 100,000 行，按 1,000 行分批流式写出；先取快照最大 ID 并写审计（行数、匹配数、是否截断）再输出。导出权限列入 P1（见 `docs/progress/m1-block2.md`）。 |
| 3 | `cmd/cassette-record` 使用 `egress.Client`，`Sink` 为 `JSONLSink`，写到 `-egress-log`（默认 `cassette-record-egress.jsonl`，权限 0600，追加）；打不开日志文件即退出。静态测试 `TestEgressClientHasSink` 要求非测试代码中的 `egress.Client{}` 字面量必须设置 `Sink`。 |
