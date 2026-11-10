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
| PR1 | 本计划；`usage_hourly` 迁移（≤20261209）；按小时聚合 worker；统计 API（overview / timeseries / 按 agent 汇总、部门范围裁剪）；用量导出 CSV + 审计 | FR-32、AC-16 | 本 PR |
| PR2 | AIGC：系统设置、消息/API 显式与隐式标识、会话导出（含标识）、相关测试 | FR-30、AC-13、演示第 8 步 | 进行中 |
| PR3 | 离线包脚本（pack / uninstall / offline-check）、install 离线模式、CI 断网冒烟 job、运维文档 | FR-35、AC-01 | 待开始 |
| PR4 | Web：概览统计看板、我的用量、对话 AIGC 角标与复制后缀、系统设置 AIGC 文案 | §9.8 / §10、演示第 15 步 | 待开始 |
| PR5 | 第七块完成报告 | — | 待开始 |

## 待确认（先按以下默认值实现，不阻塞开发）

1. **统计权限点**：默认复用 `budget.read`（部门管理员子树只读），不新增 `stats.read`。备选：独立权限点。
2. **小时预聚合延迟**：worker 每 10 分钟聚合「上一完整小时 + 当前小时增量」；看板读小时表，必要时对当前小时用 `usage_records` 补齐。备选：每次看板实时聚合（数据量大时慢）。
3. **导出上限**：单次最多 10 万行（与 D-38 出站导出一致）；超出截断并在响应头标明。导出与查看共用 `budget.read`，导出必记审计；单独导出权限列为 P1。
4. **AIGC 显式文案默认**：角标「AI 生成」；复制/导出后缀「本内容由人工智能生成（衡台 HengDesk）」；管理员可改文案但**不能关闭**标识。备选：允许组织关闭显式标识（需留存申请日志 ≥6 个月，办法第九条）——M1 不做。
5. **隐式标识 Label 取值**：默认 `1`（表示属于人工智能生成合成内容，按办法常见枚举）；ContentProducer 默认「衡台 HengDesk」；ProduceID=`request_id`；传播字段 M1 留空（私有化非传播平台）。备选：对接网信办编码体系（待合规材料）。
6. **会话导出格式**：默认 Markdown（含显式后缀 + HTML 注释元数据）；可选 `format=json`。备选：只做 JSON。
7. **卸载默认行为**：`uninstall.sh` 默认只 `compose down`，加 `--volumes` 才删命名卷；`secrets/` 与宿主机备份目录默认保留。备选：交互确认后连同 secrets 删除。
8. **断网 CI 镜像来源**：job 内先在有网阶段 build/pull 并 `docker save`，再在 `--network none` 的步骤里 `docker load` + install；不把 tar 上传为 artifact（太大）。备选：用预先缓存的 GH Actions cache（体积与权限不确定，先不做）。

## 进度记录

（每个 PR 合并后在这里补一条：PR 号、squash 提交、内容摘要。）

- PR1：计划文档；`usage_hourlies` 迁移 `20261128000000_m1_stats_pr1.sql`；`usage.RebuildHours` / worker 每 10 分钟；`GET /admin/stats/{overview,timeseries,summary,export}`；`group_by=agent`；`budget.read` 部门子树裁剪；导出记 `usage.export` 审计。

- PR2（进行中）：`internal/aigc`；`GET/PUT /admin/aigc`、`GET /aigc`；消息 `aigc`/`aigc_badge`；`X-HengDesk-AIGC` 响应头；会话导出 md/json（显式后缀+元数据）；审计 `conversation.export` / `aigc.settings.update`。
