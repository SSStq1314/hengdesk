# M1 第八块：端到端演示脚本与 M1 验收自测（进度与拆分）

依据 PRD §13 M1 退出标准的 15 步端到端演示剧本、§14 AC-01～AC-17，以及 decisions.md 的 D-30～D-46。本块在第一至第七块均已合入 main（`5e3c970`，第七块完成报告 #64）的基础上，把演示剧本做成可重复运行的验收脚本，补齐对照表中尚未被端到端覆盖的断言，并写出给验收人看的演示手册。

迁移时间戳若本块需要新增，占用 `20261211`–`20261219`（并行 D-46 从 `20261220` 起）。合并前若 main 已有更大版本，将本块迁移改名到 main 最大版本之后并重算 `atlas.sum`。当前计划以脚本、CI、文档和就地缺陷修复为主，不预先新增迁移。

并行：`/workspace/hengdesk-d46` 正在落实 D-46 增量（标识文案校验、Markdown 导出 front matter、只能导出自己的会话、`uninstall --volumes` 交互确认）。端到端脚本里涉及这些行为的断言**按 D-46 书写**；若合入本块时 D-46 尚未进 main，对应步骤标为阻塞，待其合入后复跑，不自行改用旧行为放行。

## 范围与技术要点

### 15 步端到端脚本（PRD §13）

脚本放在 `tests/acceptance/`，用 Go 对已安装实例发 HTTP 请求（cookie 会话 + CSRF），不嵌入 server 内部测试夹具，以贴近「非开发人员按文档验收」。入口是 `tests/acceptance/run.sh`：

1. 可选：用正式离线包 `install.sh --offline --up` 安装（或复用已启动的 compose）。
2. 在同一 Docker 网络拉起演示依赖：`mockupstream`（国产/本地模型均指向它）、`mockmcp`（`query_ticket` / `create_ticket`）、轻量 **mock OIDC**（见下方待确认）。三者打成 `hengdesk/mocks` 镜像，不进入默认生产镜像。
3. 按 §13 第 1–15 步顺序执行，每步有断言；失败即停并打印步骤号、请求与响应摘要（不含密钥）。
4. 穿插关键反例：未登录 401、越权 404、审计 fail-closed、AC-08 重启恢复（`docker compose restart` 服务进程）、D-46 文案/导出/卸载确认。

CI 只用 mock 上游，不读取 `DEEPSEEK_API_KEY`，不把密钥写入仓库、CI 日志或 `.env`。真实 DeepSeek 联调只在本机、有环境变量时可选跑一次，只记录通过与否。

### 断网 CI 与正式包干净安装（AC-01 / D-46 第 8 条）

- 已有 job「断网安装冒烟（AC-01）」只验证 load + healthz + 登录。本块新增（或扩展）job：在同样的代理黑洞 / `--pull never` 环境里跑完整 15 步，并断言安装与脚本运行期间无外连。
- D-46 新增验收项：用**正式发布的离线包**（`deploy/pack-offline.sh` 产物，不是 CI 临时 `docker save` 目录）在一台干净、断网的机器上完整安装一次并留下记录。本块尽量用干净容器/目录模拟（新目录解包、删除本地应用镜像、代理黑洞）；若本机不是「全新服务器」，在 `docs/demo/` 写清与真正干净机器的差异，并把「验收时在干净断网机上重跑并归档记录」列为人工步骤。

### 演示手册

`docs/demo/m1-acceptance.md`：在离线 x86 机器上一步步怎么跑、每步预期看到什么（界面与脚本输出对照）、失败时看哪些日志。给验收人看，不假定会编译代码。

## AC-01～AC-17 对照表

状态取值：**通过**（已有自动化且 CI 绿）、**未覆盖**（本块要补端到端或记录）、**部分**（集成已通过，端到端或 D-46 增量未齐）、**阻塞**（缺凭据或依赖未合入）。

| AC | 含义 | 对应实现 PR（main） | 已有自动化测试 / 脚本 | 当前状态 | 本块计划 |
|---|---|---|---|---|---|
| AC-01 | 离线安装，出站为 0 | #62 | CI job「断网安装冒烟（AC-01）」；`deploy/pack-offline.sh` / `install.sh --offline` | 部分：CI 冒烟通过；正式包在干净断网机上的安装记录未做 | 15 步脚本第 1 步；新 CI job 跑完整剧本；`docs/demo/` 留下本机模拟记录并写明差异（D-46 第 8 条） |
| AC-02 | SSO 首次登录自动建号、归入部门、记审计 | #12 及后续 OIDC（`oidc_test.go` / `oidc_d34_test.go`） | `TestOIDCAutoCreateAndLinkBySub`、`TestOIDCDeptMappingByPath` / `ByCode` | 部分：集成通过；演示包无 Keycloak；OIDC 仅能通过环境变量启用 | 脚本第 2 步：mock OIDC + 部门/人员 CSV；断言建号、部门、审计。Keycloak 见待确认 |
| AC-03 | 未授权智能体列表不可见、详情 404 | #53、#55、#60 | `TestAgentCRUDPublishVisibility`、`TestAgentDeptScope`、`TestD45RuntimeIntersection` | 部分：集成通过 | 脚本第 11 步（市场部 B） |
| AC-04 | 文件级 RAG：无权限者检索/上下文/引用都不含该文件 | #37–#45 | `TestKBRetrieveACL`、`TestChatKBCitations`、`kb/recall` 越权为 0 | 部分：集成通过 | 脚本第 8 步（A 问差旅，不含高管文件） |
| AC-05 | ACL 即时生效，无需重建索引 | 第四块 PR2 | `TestKBRetrieveACL`（自定义文件 ACL 后立即命中） | 部分：集成通过 | 脚本在第 8 步后补：授予 B 高管文件权限后再问一次，能检索到 |
| AC-06 | 需审批工具在通过前上游调用数为 0，通过后恰好 1 次 | #46–#52、#54 | `TestApproveExecutesExactlyOnceWithSnapshot`、`TestApprovalCenterEndToEnd`、`TestD44AutoContinueOnApprove` | 部分：集成通过 | 脚本第 9 步（对照 mockmcp 调用计数） |
| AC-07 | 拒绝或超时后模型收到「已拒绝/已超时」，并记审计 | #49、#54 | `TestRejectWithdrawExpire`、`TestD44AutoContinueOnRejectAndExpire` | 部分：集成通过 | 脚本第 10 步（C 拒绝） |
| AC-08 | 挂起审批时重启 server，通过后对话能继续 | #54（D-44） | `TestD44RestartWhileAwaiting`、`TestD44RestartDuringContinuation`（进程内重启） | 部分：集成通过；未在 compose 真重启上覆盖 | 脚本反例：等待审批时 `compose restart server`，再由 C 通过并续写 |
| AC-09 | 白名单外域名与链路本地地址被阻断，记出站日志 | #28–#31、#47 | `TestMCPEgressWhitelistAndHeaders`、`TestInternalAndMetadataBlocked`、`TestGatewayEgressEndToEnd` | 部分：集成通过 | 脚本第 12 步（mockmcp 演示开关访问白名单外地址） |
| AC-10 | 预算 80% 告警、100% 拦截并返回可读错误 | #27 | `TestBudgetBlockAlertReconcile`、`TestBudgetAlertStateAndRebuild` | 部分：集成通过 | 脚本第 13 步（财务部小额度 X） |
| AC-11 | 审计员按 trace 回放完整链路 | #36 等 | `TestAuditTraceReplay`、`TestAuditDeniedLoginAndQuery` | 部分：集成通过 | 脚本第 14 步（D 回放 A 的 8–13 步） |
| AC-12 | 审计不可改删（含超管）；失败尝试本身入审计 | #8–#11；CI 审计表 append-only | `TestHashChainVerifyAndTamper`、CI「审计表 append-only」；应用无删除/修改审计 API | 部分：集成与 CI 通过 | 脚本第 14 步：D 改配置 404；超管删审计失败（API 不存在或被拒），尝试被记录 |
| AC-13 | 查看/复制/导出/API 均带显式或元数据标识 | #61、#63 | `TestAIGCSettingsAndExport`、`TestDefaultsValidateSave`、`TestAppendSuffixAndMeta` | 部分：集成通过；D-46 要求更严的文案校验与 Markdown front matter 尚未按拍板落地 | 脚本第 8 步与 D-46 断言：后缀、`X-HengDesk-AIGC`、JSON 元数据、**md front matter**；空文案或不含「人工智能生成 / AI 生成」保存失败 |
| AC-14 | 本地 OpenAI 兼容端点 + 国产 API，「测试」可流式对话并记 usage | #25、#26 | `TestModelTestConnection`、`TestChatNonStreamSSEAndACL`（mock） | 部分：mock 通过；真实通义 / vLLM 缺凭据；Ollama 未验证；DeepSeek 仅本机可选 | 脚本第 3 步对 mockupstream 点「测试」。真实上游见阻塞/待办 |
| AC-15 | 任何角色查看配置或 API 都看不到密钥明文 | #25 | `TestModelAdminCRUDAndKeyNeverReturned`、`TestChatUpstream401Redacted` | 部分：集成通过 | 脚本第 3 步：创建供应商后 GET 响应无明文 key |
| AC-16 | 看板与 `usage_record` 汇总 token 一致，估算单独标注 | #59、#63 | `TestStatsOverviewScopeExport`、`TestRebuildHours`、`TestSummaryByAgent` | 部分：集成通过 | 脚本第 15 步：财务部看板 vs 明细汇总 |
| AC-17 | 发布构建许可证扫描不含白名单之外许可 | CI `license scan + SBOM` | 该 job 本身 | 通过（每个 PR 门禁） | 本块 PR 继续走该门禁；不新增例外 |
| D-46 安装记录 | 正式离线包在干净断网机上完整安装并留记录 | #62 提供打包/安装；记录尚未有 | 无 | 未覆盖 | `docs/demo/` 归档本机模拟记录；真正干净机器列为验收人工步骤 |
| D-46 卸载 | `--volumes` 须交互确认（非交互加 `--yes`），并提示先备份；`secrets/` 保留 | #62 已有 `-y` / `--volumes`；备份提示与文案可能未齐 | 脚本层手工/本块补测 | 部分 | 验收脚本在收尾对 `uninstall.sh` 做断言（无 `--yes` 时 `--volumes` 拒绝；有确认则删卷、保留 secrets） |
| D-46 导出权限 | 只能导出自己的会话 | #61 `conv_export` 已走 `own()` | 会话属主测试有，导出越权需在脚本中显式断言 | 部分 | 脚本：B 导出 A 的会话返回 404 |

## PR 拆分

| PR | 内容 | 对应 | 状态 |
|---|---|---|---|
| PR1 | 本计划与 AC 对照表 | 本块起步 | 本 PR |
| PR2 | `tests/acceptance/`：run.sh、15 步 Go 脚本、testdata、`hengdesk/mocks` 与 `docker-compose.acceptance.yml` | §13 第 1–15 步，AC-01～AC-16 端到端，D-46 断言 | 待开始 |
| PR3 | CI job：断网安装环境跑完整 15 步，断言无外连；必要时把 mocks 打进验收用离线包 | AC-01、D-46 安装项的自动化部分 | 待开始 |
| PR4 | `docs/demo/m1-acceptance.md` 演示手册 + 本机模拟干净安装记录（含与真正干净机器的差异） | 验收人手册 | 待开始 |
| 缺陷修复 | 跑脚本时发现的缺陷就地修，每个缺陷一个小 PR；AC 未覆盖则补实现或测试 | 视发现而定 | 按需 |
| 完成报告 | 第八块完成报告（含对照表最终状态、演示方式、待确认、阻塞） | — | 最后 |

## 待确认（先按以下默认值实现，不阻塞开发）

1. **演示 IdP**：PRD 写「Keycloak 演示 IdP，随演示包提供」。M1 验收脚本默认用仓库内轻量 **mock OIDC**（可打进验收离线包，体积小、可断网）。Keycloak 镜像体积大，不打进默认离线包；若产品要求必须用 Keycloak，列为验收时可选手动步骤或 P1 演示附加包。
2. **OIDC 配置入口**：当前只能通过 `HENGDESK_OIDC_*` 环境变量在启动时启用，安装向导里没有图形化配置。脚本与手册默认：在 `deploy/.env` 写入配置并重启 server。运行时管理界面列为 P1。
3. **mocks 是否进入正式离线包**：默认正式包（`pack-offline.sh`）不含 mocks；`pack-offline.sh --with-acceptance`（或独立 `pack-acceptance.sh`）才打入 mocks 镜像、compose 覆盖件和验收脚本。AC-01 的「正式包」安装记录用默认包；15 步剧本用验收包或在正式包安装后再加载 mocks。
4. **市场部只能用本地模型**：脚本按此设置模型 ACL。两个「测试」端点在 CI 中都指向 mockupstream，用不同 provider 名称区分「国产 API」与「本地」；不在 CI 里打真实 DeepSeek / Ollama。
5. **财务部预算 X**：取演示用小值（默认 200 tokens，80% 告警、100% 拦截），以便第 13 步尽快触达，不依赖长对话。
6. **AC-05 插入位置**：剧本原文无单独一步，脚本在第 8 步后追加「授予后再问」，不改变第 1–15 步编号。
7. **通义、vLLM、Ollama**：缺凭据或未验证的，对照表标阻塞/待办，不挡本块合入。本机若能拉起 Ollama 小模型可顺手验证，结果写入完成报告。

## 进度记录

（每个 PR 合并后在这里补一条：PR 号、squash 提交、内容摘要。）

- PR1（本 PR）：第八块计划与 AC-01～AC-17 / D-46 对照表。
