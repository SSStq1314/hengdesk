# D-46 统计 / AIGC 标识 / 离线安装包（产品经理定）

> 同步自 `ai-office-platform/decisions.md` 第 459–469 行（产品经理，2026-11-10，main 5e3c970，依据 docs/progress/m1-block7.md「待确认」）。下面是原文，未作改写；实现说明在后面。

1. 统计查看复用 budget.read，不新增 stats.read；部门管理员仅看本部门子树；无权限者只看「我的用量」。确认。补充：个人明细属于敏感数据，查看他人明细计入审计。
2. worker 每 10 分钟做小时预聚合，看板读小时表，当前小时用明细补齐。确认。补充：日/月边界按 D-37 的系统时区（默认 Asia/Shanghai）切分。
3. 导出单次上限 10 万行（与 D-39 一致），与查看共用 budget.read，必须记审计。确认。审计需记录筛选条件和行数。
4. 显式标识：对话角标「AI 生成」；复制/导出加后缀「本内容由人工智能生成（衡台 HengDesk）」；管理员可改文案、不可关闭。确认。补充：改后的文案不能为空，且必须包含「人工智能生成」或「AI 生成」，否则保存失败；改文案记审计。
5. 隐式标识：Label=1，ContentProducer=衡台 HengDesk，ProduceID=request_id，传播方字段留空。确认。JSON 导出放在元数据字段里；Markdown 导出放在文件开头的 front matter 里。
6. 会话导出默认 Markdown，可选 JSON。确认。只能导出自己的会话；导出记审计；处于法律保留的会话也可以导出，但内容不会因此改变。
7. uninstall.sh 默认只停服务；--volumes 才删数据卷；secrets/ 始终保留。确认。补充：--volumes 需要交互输入确认（非交互模式要求 --yes），并提示先做备份；secrets/ 中有 D-36 的主密钥，保留它是必须的。
8. 断网安装的 CI 冒烟在有网阶段导出镜像，不把镜像包作为构建产物保留。确认。补充：M1 验收前，要用正式发布的离线包在一台干净、断网的机器上完整安装一次并留下记录，作为验收项。

P1/开放项（组织能否关闭显式标识、独立的导出权限、网信办编码、ARM64 离线包、License、数字水印）全部不进 M1。

## 实现说明（工程）

| 条目 | 实现 | 测试 |
|---|---|---|
| 1 查看他人明细记审计 | `GET /admin/usage/records` 在过滤条件不是「仅本人」时记 `usage.detail.read`（含筛选与行数）；审计失败则拒绝（fail-closed）。汇总看板不按个人明细记。 | `TestD46UsageDetailAuditAndDayBoundary` |
| 2 日/月边界按系统时区 | 日期参数与默认近 7 天、timeseries 分桶一律用 `HENGDESK_TIMEZONE`（默认 Asia/Shanghai），不再用容器 `time.Local`。小时聚合沿用 `HourBucket`。 | `TestParseDateParamTimezone`、`TestHourBucketTimezoneBoundary`、`TestD46UsageDetailAuditAndDayBoundary` |
| 3 导出审计含筛选与行数 | 既有 `usage.export` 的 Detail 已含 `rows` / `truncated` / `from` / `to` / `user_id` / `department_ids` / `model_id` / `agent_id`；本决策补测试断言。 | `TestStatsOverviewScopeExport` |
| 4 标识文案校验 | 角标与后缀不能为空，必须包含「人工智能生成」或「AI 生成」，否则 400；`aigc.settings.update` 记审计。前端同步提示。 | `TestDefaultsValidateSave`、`TestAIGCSettingsAndExport` |
| 5 导出隐式标识位置 | JSON 放在 `aigc` 元数据字段；Markdown 改为文件开头的 YAML front matter（不再用 HTML 注释）。 | `TestAppendSuffixAndMeta`、`TestAIGCSettingsAndExport` |
| 6 只能导出自己的会话 | 沿用 `own()`：他人 / 不存在 404，未登录 401；导出记 `conversation.export`（审计失败 503，fail-closed）。法律保全会话仍可导出且内容不变。 | `TestAIGCSettingsAndExport` |
| 7 uninstall.sh 确认与 secrets | `--volumes` 提示先备份并交互确认；非交互必须 `--yes`，否则非 0 退出；`secrets/` 始终保留。 | `deploy/uninstall_test.sh` |
| 8 干净断网机完整安装并留记录 | **已列为 M1 验收项**：写入 `docs/progress/m1-block8.md` AC 对照表「D-46 安装记录」行，由第八块执行并归档记录。CI 断网冒烟仍按本条前半确认保留。 | —（人工验收项） |
| P1 / 开放项 | **仅文档记录，不进 M1，本 PR 不实现。** | — |

