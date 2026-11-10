# M1 第七块：统计 / AIGC 标识 / 离线安装包（进度与拆分）

依据 PRD §8 的 FR-30（AIGC 标识）、FR-32（用量看板）、FR-35（Docker Compose + x86_64 离线安装包）、FR-40 中与系统设置相关的 AIGC 文案；§9.7（AIGC）、§9.8（使用统计）、§9.9（私有化部署）；§10（`/admin/dashboard`、`/me/usage`、系统设置 AIGC）；§13 演示剧本第 8、15 步；AC-01（离线安装出站为 0）、AC-13（AIGC 标识）、AC-16（统计与 usage_record 一致）。以及 decisions.md 的 D-03（统计只用 PostgreSQL，不做 ClickHouse）、D-08 / D-16（x86 离线 Compose）、D-12 / D-22（AIGC 显式 + 元数据隐式）、D-28（默认 SeaweedFS）、D-30（安装可选 HTTPS）、D-37（估算用量单独标注）。本块只做 P0；ARM64（FR-36）、License（FR-38）、数字水印形式的内容隐式标识列为 P1/开放问题。

迁移时间戳占用 `20261128`–`20261209`（并行 D-45 智能体停用/回滚从 `20261210` 起）。合并前若 main 已有更大版本，将本块迁移改名到 main 最大版本之后并重算 `atlas.sum`。不改动智能体停用/回滚逻辑。

## 范围与技术要点

### 用量 / 成本统计（FR-32 / AC-16）

- **数据源**：`usage_records` 为权威（第二块已建，第六块补 `agent_id`）。按小时预聚合到 `usage_hourly`（PostgreSQL，D-03），由 worker 定时任务写入；看板优先读小时表，明细仍读 `usage_records`。
- **指标**：活跃用户数、对话数、调用次数、token（prompt/completion/total）、折算成本（微元）、估算用量单独标注（`estimated_*` / `unknown_calls`）。
- **维度**：按人、部门、模型、智能体、时间（小时 / 日趋势）；Top 用户表。
- **权限**：复用 `budget.read`（组织管理员看全部；部门管理员只看本部门子树，与预算只读一致，PRD §9.8）。无权限 404；未登录 401。导出记审计（`usage.export`，fail-closed：审计写失败则不导出）。
- **时区**：服务器配置时区（`HENGDESK_TIMEZONE`，默认 Asia/Shanghai）；小时桶按此时区切分。
- **已有接口**：`GET /admin/usage/summary`、`/admin/usage/records`、`GET /usage/me` 保留并增强（补 `group_by=agent`、部门范围强制裁剪、看板专用 `GET /admin/stats/overview` / `timeseries` / `export`）。

### AIGC 标识（FR-30 / AC-13）

对照《人工智能生成合成内容标识办法》与 GB 45438-2025（开放问题 Q4 仍在，本块按保守可验收默认落地）：

- **交互场景界面显式标识**：每条 assistant 消息旁持续显示角标「AI 生成」（含「AI」+「生成」要素）；对话页底部可配置提示文案（系统设置）。
- **复制 / 导出显式标识**：复制单条或导出会话时，在文本末尾附加可配置文字标识（默认 `\n\n—— 本内容由人工智能生成（衡台 HengDesk）`）。
- **隐式标识（文件元数据）**：
  - API：assistant 消息响应与导出会话响应头写入 `X-HengDesk-AIGC`（JSON，字段名含 AIGC，含 Label / ContentProducer / ProduceID / ReservedCode1 等，对齐附录 E 子集）。
  - 导出文件：Markdown / JSON 导出在文件头写入 HTML 注释或 JSON 字段承载同一元数据；ProduceID 使用消息 `request_id`（或会话+消息 ID）。
- **不做**：图片/音视频水印（M1 无此生成能力）；用户申请「去掉显式标识」的开关（M1 一律标识，避免合规缺口，列为待确认）。
- **设置**：`system_settings` 键 `aigc`：显式文案、复制后缀、ContentProducer 名称（默认「衡台 HengDesk」）；改设置记审计，需 `system.settings`。

### x86 离线安装包（FR-35 / AC-01）

- **已有**：`deploy/install.sh`、`upgrade.sh`、`restore.sh`、compose 与 Dockerfiles；CI 已有在线 compose 冒烟。
- **本块补齐**：
  - `deploy/pack-offline.sh`：构建并 `docker save` 所需镜像为 tar（不提交大文件到仓库），打包 `install.sh` / `upgrade.sh` / `restore.sh` / `uninstall.sh` / compose / `.env.example` / 自检脚本；输出到 `dist/`（gitignore）。
  - `deploy/uninstall.sh`：停止并移除本 compose 项目容器与命名卷（需确认；默认保留 `secrets/` 与备份目录）。
  - `deploy/offline-check.sh`：安装前自检（Docker / 磁盘 / 端口 / 架构 x86_64）。
  - install 路径：离线模式 `docker load -i images/*.tar` 后 `compose up`（不 `--build`、不 pull）。
  - **CI「断网安装冒烟」**：在已有镜像的 runner 上，用 `docker compose ...` 或包装脚本在 `--network none` 环境执行 load+up+healthz，断言安装过程无外连（AC-01）。包体不进仓库。

## PR 拆分

| PR | 内容 | 对应 | 状态 |
|---|---|---|---|
| PR1 | 本计划；`usage_hourly` 迁移（≤20261209）；按小时聚合 worker；统计 API（overview / timeseries / 按 agent 汇总、部门范围裁剪）；用量导出 CSV + 审计 | FR-32、AC-16 | 已合并 #59 |
| PR2 | AIGC：系统设置、消息/API 显式与隐式标识、会话导出（含标识）、相关测试 | FR-30、AC-13、演示第 8 步 | 已合并 #61 |
| PR3 | 离线包脚本（pack / uninstall / offline-check）、install 离线模式、CI 断网冒烟 job、运维文档 | FR-35、AC-01 | 已合并 #62 |
| PR4 | Web：概览统计看板、我的用量、对话 AIGC 角标与复制后缀、系统设置 AIGC 文案 | §9.8 / §10、演示第 15 步 | 已合并 #63 |
| PR5 | 第七块完成报告 | — | 本 PR |

## 待确认（已定，见 D-46）

产品经理已在 decisions.md D-46 拍板（2026-11-10）。8 条默认值全部确认，第 1、4、5、6、7 条有补充，由本仓库 `docs/decisions/D-46-stats-aigc-offline.md` 与落地 PR 实现。P1 / 开放项（组织能否关闭显式标识、独立的导出权限、网信办编码、ARM64 离线包、License、数字水印）全部不进 M1。第 8 条补充的验收项「用正式发布的离线包在干净、断网的机器上完整安装一次并留下记录」由第八块负责，本块不实施。

1. **统计权限点**：复用 `budget.read`（部门管理员子树只读），不新增 `stats.read`。**已定。补充：查看他人个人明细记审计。**
2. **小时预聚合延迟**：worker 每 10 分钟聚合「上一完整小时 + 当前小时增量」；看板读小时表，必要时对当前小时用 `usage_records` 补齐。**已定。补充：日/月边界按 D-37 系统时区（默认 Asia/Shanghai）切分。**
3. **导出上限**：单次最多 10 万行；与查看共用 `budget.read`，导出必记审计。**已定。补充：审计需记录筛选条件和行数。** 单独导出权限列为 P1，不进 M1。
4. **AIGC 显式文案默认**：角标「AI 生成」；复制/导出后缀「本内容由人工智能生成（衡台 HengDesk）」；管理员可改文案但不能关闭标识。**已定。补充：改后文案不能为空，且必须包含「人工智能生成」或「AI 生成」，否则保存失败；改文案记审计。** 组织关闭显式标识不进 M1。
5. **隐式标识 Label 取值**：默认 `1`；ContentProducer 默认「衡台 HengDesk」；ProduceID=`request_id`；传播字段 M1 留空。**已定。补充：JSON 导出放在元数据字段；Markdown 导出放在文件开头的 YAML front matter。** 对接网信办编码不进 M1。
6. **会话导出格式**：默认 Markdown，可选 `format=json`。**已定。补充：只能导出自己的会话（他人 404）；导出记审计；法律保全会话也可导出。**
7. **卸载默认行为**：`uninstall.sh` 默认只停服务，加 `--volumes` 才删命名卷；`secrets/` 始终保留。**已定。补充：`--volumes` 须交互确认（非交互必须 `--yes`），并提示先备份；secrets/ 含 D-36 主密钥，必须保留。**
8. **断网 CI 镜像来源**：job 内先在有网阶段导出镜像，不把 tar 作为构建产物保留。**已定。补充（第八块验收）：用正式发布的离线包在干净、断网的机器上完整安装一次并留记录。**

## 进度记录

（每个 PR 合并后在这里补一条：PR 号、squash 提交、内容摘要。）

- PR1 #59 → `59b2bbf`：计划文档；`usage_hourlies` 迁移 `20261128000000_m1_stats_pr1.sql`；`usage.RebuildHours` / worker 每 10 分钟；`GET /admin/stats/{overview,timeseries,summary,export}`；`group_by=agent`；`budget.read` 部门子树裁剪；导出记 `usage.export` 审计。
- PR2 #61 → `0185fb4`：`internal/aigc`；`GET/PUT /admin/aigc`、`GET /aigc`；消息 `aigc`/`aigc_badge`；`X-HengDesk-AIGC` 响应头；会话导出 md/json（显式后缀+元数据）；审计 `conversation.export` / `aigc.settings.update`。
- PR3 #62 → `1779937`：pack-offline / offline-check / uninstall；install --offline；CI 断网冒烟（显式 pull 依赖镜像、健康检查绕过 HTTP_PROXY）；`docs/ops/offline-install.md`。
- PR4 #63 → `be55057`：用量看板、`/me/usage`、AIGC 设置页、对话角标与复制后缀。
- PR5（本 PR）：第七块完成报告。
- D-46：统计 / AIGC / 离线包拍板落地（见本决策后续 PR；合并后补 PR 号与 squash 提交）。

## 第七块完成报告（PR5）

### PR 与合并提交

| PR | 内容 | squash 提交 |
|---|---|---|
| #59 | PR1：计划、`usage_hourly` 迁移与小时预聚合 worker、统计 API（overview / timeseries / summary / export）、部门子树裁剪、导出审计 | `59b2bbf` |
| #61 | PR2：AIGC 系统设置、消息显式角标、复制/导出后缀、`X-HengDesk-AIGC` 隐式元数据、会话导出 md/json | `0185fb4` |
| #62 | PR3：x86 离线安装包脚本、`install --offline`、CI 断网冒烟、运维文档 | `1779937` |
| #63 | PR4：Web 用量看板、我的用量、AIGC 设置页、对话角标与复制后缀 | `be55057` |
| PR5 | 本完成报告 | 见本 PR 合并提交 |

写这份报告时，main 为 `be55057`。并行合入 D-45 #60（`3fef6a2`），新增迁移 `20261210000000_m1_agent_d45.sql`（智能体停用/回滚）。本块唯一新迁移 `20261128000000_m1_stats_pr1.sql` 已在 #59 合入、且时间戳早于 D-45，无需改名或重算 `atlas.sum`。本块未改动智能体停用/回滚逻辑。

### 对应需求

| 需求 | 状态 |
|---|---|
| FR-32 用量看板（人 / 部门 / 模型 / 智能体、趋势与成本） | 已完成（#59、#63）；权威数据源 `usage_records`，小时预聚合 `usage_hourly`；`budget.read` 部门子树；无权限 404、未登录 401 |
| AC-16 统计与 usage_record 一致 | 已完成（#59）；看板读小时表，当前小时可用明细补齐；估算用量单独标注 |
| FR-30 / AC-13 AIGC 显式 + 隐式标识 | 已完成（#61、#63）；角标不可关闭；复制/导出附加后缀；API 与导出写入附录 E 子集元数据 |
| FR-35 / AC-01 x86 离线安装包、断网安装出站为 0 | 已完成（#62）；`pack-offline.sh` 生成 tar（不进仓库）；CI 在代理黑洞下 `--offline --up` 后登录成功 |
| §10 `/` 看板、`/me/usage`、系统设置 AIGC | 已完成（#63） |
| 演示剧本第 8、15 步 | 见下方演示方式 |

### 测试情况

- 每个 PR 都是 GitHub Actions 全绿后才 squash 合并：Go lint + test（-race）、PostgreSQL 17 + pgvector 集成、govulncheck、许可证扫描 + SBOM、Web lint + typecheck + build + 文案集中检查、compose 冒烟（SeaweedFS / MinIO / HTTPS）。#62 / #63 另含「断网安装冒烟（AC-01）」。
- 本块新增 / 扩展测试：`internal/usage.TestRebuildHours` / `TestSummaryByAgent`；`internal/api.TestStatsOverviewScopeExport`（部门裁剪、导出审计 `usage.export`）、`TestUsageSummaryBudgetRead`；`internal/aigc.TestDefaultsValidateSave` / `TestAppendSuffixAndMeta`；`internal/api.TestAIGCSettingsAndExport`（设置权限、导出后缀与隐式元数据）。
- CI 只用 mock 模型上游，不读取任何真实密钥、也不引入 DEEPSEEK。前端页面只有 lint / typecheck / build，没有端到端测试。
- 本地 Go 须 1.26.x（`GOTOOLCHAIN=go1.26.10`）。

### 演示方式

1. 启动环境（`cd deploy && docker compose up -d`）。演示模型可用 `cd server && go run ./cmd/mockupstream`。
2. 有 `budget.read` 的组织管理员打开工作台 `/`：查看活跃用户、对话数、token、折算成本，日趋势与 Top 用户，按日期导出 CSV（审计出现 `usage.export`）。部门管理员只看到本部门子树；无该权限的成员看到引导，前往「我的用量」。
3. 普通成员打开 `/me/usage`，按模型查看本人调用与成本。
4. 任意对话中，助手回复持续显示「AI 生成」角标；点「复制」后剪贴板文本末尾带「本内容由人工智能生成（衡台 HengDesk）」；导出会话（md / json）同样带显式后缀与隐式元数据，响应头含 `X-HengDesk-AIGC`（演示剧本第 8 步）。
5. 有 `system.settings` 的管理员打开 `/aigc`，可改角标、后缀、ContentProducer、Label，但不能关掉标识。
6. 离线安装（演示 / AC-01）：在有网机构建 `./deploy/pack-offline.sh --version <ver>`，把包拷到离线机后 `./install.sh --offline --up`（不 `--build`、不 pull）。文档见 `docs/ops/offline-install.md`。

### 迁移

- `20261128000000_m1_stats_pr1.sql`：`usage_hourlies` 小时预聚合表（本块唯一新增迁移）。
- 并行 D-45：`20261210000000_m1_agent_d45.sql` 已在 main，不属于本块；本块未改该文件，也未改智能体停用/回滚逻辑。

### 待确认（已定，D-46）

计划中的 8 条默认值已由产品经理在 D-46 确认，并带补充（查看他人明细审计、日界按系统时区、导出审计含筛选与行数、标识文案校验、Markdown front matter、会话仅本人可导出、卸载确认与保留 secrets）。落地见后续 D-46 PR。P1 / 开放项（组织关闭显式标识、独立导出权限、网信办编码、ARM64 离线包、License、数字水印）全部不进 M1。第 8 条「干净断网机用正式离线包完整安装并留记录」由第八块做验收项。
