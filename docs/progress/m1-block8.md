# M1 第八块：端到端演示脚本与 M1 验收自测（进度与拆分）

依据 PRD §13 M1 退出标准的 15 步端到端演示剧本、§14 AC-01～AC-17，以及 decisions.md 的 D-30～D-49。本块在第一至第七块均已合入 main（当时 `5e3c970`，第七块完成报告 #64）的基础上，把演示剧本做成可重复运行的验收脚本，补齐对照表中尚未被端到端覆盖的断言，并写出给验收人看的演示手册。

迁移时间戳若本块需要新增，占用 `20261211`–`20261219`。并行任务占用其后版本，**本块不改这些并行分支的写入代码**：

- D-46（`/workspace/hengdesk-d46`）：标识文案校验、Markdown 导出 front matter、只能导出自己的会话、`uninstall --volumes` 交互确认；迁移自 `20261220` 起。
- 审计链多进程写入误拒修复（`/workspace/hengdesk-audit`，D-47）：迁移自 `20261225` 起。

合并前若 main 已有更大版本，将本块迁移改名到 main 最大版本之后并重算 `atlas.sum`。当前计划以脚本、CI、文档和就地缺陷修复为主，不预先新增迁移。

端到端脚本里涉及 D-46 行为的断言**按 D-46 书写**；若合入本块时 D-46 尚未进 main，对应步骤标为阻塞，待其合入后复跑，不自行改用旧行为放行。

## 范围与技术要点

### 15 步端到端脚本（PRD §13）

脚本放在 `tests/acceptance/`，用 Go 对已安装实例发 HTTP 请求（cookie 会话 + CSRF），不嵌入 server 内部测试夹具，以贴近「非开发人员按文档验收」。入口是 `tests/acceptance/run.sh`：

1. 可选：用正式离线包 `install.sh --offline --up` 安装（或复用已启动的 compose）。
2. 在同一 Docker 网络拉起演示依赖：`mockupstream`（国产/本地模型均指向它）、`mockmcp`（`query_ticket` / `create_ticket`）、轻量 **mock OIDC**（见下方待确认）。三者打成 `hengdesk/mocks` 镜像，不进入默认生产镜像。
3. 按 §13 第 1–15 步顺序执行，每步有断言；失败即停并打印步骤号、请求与响应摘要（不含密钥）。
4. 穿插关键反例：未登录 401、越权 404、审计 fail-closed、AC-08 重启恢复（经 docker.sock 或 `-restart-cmd` 重启 server 进程）、D-46 文案/导出/卸载确认。

CI 只用 mock 上游，不读取 `DEEPSEEK_API_KEY`，不把密钥写入仓库、CI 日志或 `.env`。真实 DeepSeek 联调只在本机、有环境变量时可选跑一次，只记录通过与否。

### 断网 CI 与正式包干净安装（AC-01 / D-46 第 8 条）

- 已有 job「断网安装冒烟（AC-01）」只验证 load + healthz + 登录。本块新增（或扩展）job：在同样的代理黑洞 / `--pull never` 环境里跑完整 15 步，并断言安装与脚本运行期间无外连。
- D-46 新增验收项：用**正式发布的离线包**（`deploy/pack-offline.sh` 产物，不是 CI 临时 `docker save` 目录）在一台干净、断网的机器上完整安装一次并留下记录。本块尽量用干净容器/目录模拟（新目录解包、删除本地应用镜像、代理黑洞）；若本机不是「全新服务器」，在 `docs/demo/` 写清与真正干净机器的差异，并把「验收时在干净断网机上重跑并归档记录」列为人工步骤。本执行环境无 Docker，15 步实跑与干净安装记录放到有 Docker 的验收机/CI（PR3 / PR4）。

### 演示手册

`docs/demo/m1-acceptance.md`：在离线 x86 机器上一步步怎么跑、每步预期看到什么（界面与脚本输出对照）、失败时看哪些日志。给验收人看，不假定会编译代码。

### 真实上游冒烟（D-47，D-49 修订）

- **通义**真实冒烟按 D-49 **从 M1 验收项中移除**（用户没有通义密钥，不再挂到 M2 W1 补测来挡住 M1）。M1 改为：按通义官方接口文档做契约测试，再用录制回放覆盖兼容层。回放样本**只能**来自 vLLM CPU 小模型的真实录制，或通义官方文档里的示例，并标明来源，**不允许凭空编造响应**。仓库里已有 `TestVendorContract*`（dashscope），但回放带仍标注「按公开文档构造，非真实录制」，**不符合 D-49，列为 M1 验收前待补项**。客户现场用客户自己的密钥做一次通义真实冒烟，写入 `docs/demo/poc-delivery.md`。
- **vLLM** CPU 小模型的真实协议冒烟**保留在 M1**：官方镜像 `vllm/vllm-openai-cpu` + 约 0.5B 小模型，脚本 `tests/acceptance/vllm-cpu-smoke.sh`，记录 `docs/demo/vllm-cpu-smoke.md`。本执行环境无 Docker，未能实跑；有 Docker 的机器按脚本跑完后补记。该冒烟需拉镜像与权重，**不进入断网 CI 必跑门禁**。既有 vLLM 契约测试仍随 `go test` 跑。
- **Ollama** 仍为待办：本机能拉起则顺手验证，否则写入完成报告。
- **DeepSeek** 规则不变：密钥只在环境变量 `DEEPSEEK_API_KEY`，绝不打印、不写进仓库/CI/`.env`/日志，只记录通过与否。

### 审计链多进程写入（D-47，M1 阻塞）

多进程（server + worker）并发写审计时的误拒是 M1 退出阻塞项，由 `/workspace/hengdesk-audit` 修复，迁移自 `20261225`。**本块不修改审计写入代码**。验收标准：server + worker **200 并发写入失败 0、全链校验通过**。合入该修复前，对照表该行保持阻塞。

## AC-01～AC-17 对照表

状态取值：**通过**（已有自动化且 CI 绿）、**未覆盖**（本块要补端到端或记录）、**部分**（集成已通过，端到端或 D-46 增量未齐）、**阻塞**（缺依赖未合入）、**挂起**（明确延期到后续迭代）。

最终状态已按 #68 / #70 / #71 / #73、`/workspace/hengdesk-demo/fix-verify.md` 复跑，以及 #69 / #72 断网 15 步 CI 更新。「本块计划」列保留立项时的安排。演示方式与仍需人工补做的项见文末完成报告。

| AC | 含义 | 对应实现 PR（main） | 已有自动化测试 / 脚本 | 当前状态 | 本块计划 |
|---|---|---|---|---|---|
| AC-01 | 离线安装，出站为 0 | #62 | CI job「断网安装冒烟（AC-01）」；`deploy/pack-offline.sh` / `install.sh --offline` | **部分**：CI「断网安装冒烟」与「断网 15 步验收」均通过（#69 run [42774002679](https://github.com/SSStq1314/hengdesk/actions/runs/42774002679)、#72 run [42776879051](https://github.com/SSStq1314/hengdesk/actions/runs/42776879051)；断网探测通过，mark 之后无 `HD_EGRESS`）。正式离线包在干净断网机上的安装记录仍待人工补做（与 D-46 安装记录为同一项） | 15 步脚本第 1 步；PR3 CI 在断网环境跑完整剧本；PR4 在 `docs/demo/` 写清与真正干净机器的差异，并把该安装记录列为 M1 验收人工步骤（与真机演示未覆盖项合并，不另开一条） |
| AC-02 | SSO 首次登录自动建号、归入部门、记审计 | #12 及后续 OIDC（`oidc_test.go` / `oidc_d34_test.go`） | `TestOIDCAutoCreateAndLinkBySub`、`TestOIDCDeptMappingByPath` / `ByCode` | **通过**：脚本第 2 步用 mock OIDC 完成首次登录建号、归入部门、记审计，已随断网 15 步 CI 通过。Keycloak 仍为可选手动步骤；OIDC 仅能通过 `HENGDESK_OIDC_*` 启用 | 脚本第 2 步：mock OIDC + 部门/人员 CSV；断言建号、部门、审计。Keycloak 见待确认 |
| AC-03 | 未授权智能体列表不可见、详情 404 | #53、#55、#60 | `TestAgentCRUDPublishVisibility`、`TestAgentDeptScope`、`TestD45RuntimeIntersection` | **通过**：脚本第 11 步；fix-verify 复跑中员工 B 仍为 404 | 脚本第 11 步（市场部 B） |
| AC-04 | 文件级 RAG：无权限者检索/上下文/引用都不含该文件 | #37–#45 | `TestKBRetrieveACL`、`TestChatKBCitations`、`kb/recall` 越权为 0 | **通过**：脚本第 8 步（A 问差旅，不含高管文件）；CI 与界面复跑均通过 | 脚本第 8 步（A 问差旅，不含高管文件） |
| AC-05 | ACL 即时生效，无需重建索引 | 第四块 PR2 | `TestKBRetrieveACL`（自定义文件 ACL 后立即命中） | **通过**：脚本在第 8 步后授予再问，断网 15 步 CI 通过 | 脚本在第 8 步后补：授予 B 高管文件权限后再问一次，能检索到 |
| AC-06 | 需审批工具在通过前上游调用数为 0，通过后恰好 1 次 | #46–#52、#54 | `TestApproveExecutesExactlyOnceWithSnapshot`、`TestApprovalCenterEndToEnd`、`TestD44AutoContinueOnApprove` | **通过**：脚本第 9 步对照 mockmcp 调用计数；CI 与界面复跑均通过 | 脚本第 9 步（对照 mockmcp 调用计数） |
| AC-07 | 拒绝或超时后模型收到「已拒绝/已超时」，并记审计 | #49、#54 | `TestRejectWithdrawExpire`、`TestD44AutoContinueOnRejectAndExpire` | **通过**：脚本第 10 步；CI 与界面复跑均通过 | 脚本第 10 步（C 拒绝） |
| AC-08 | 挂起审批时重启 server，通过后对话能继续 | #54（D-44） | `TestD44RestartWhileAwaiting`、`TestD44RestartDuringContinuation`（进程内重启） | **通过**：脚本在等待审批时经 compose 重启 server，再由 C 通过并续写；断网 15 步 CI 通过 | 脚本反例：等待审批时经 docker.sock / `-restart-cmd` 重启 server，再由 C 通过并续写 |
| AC-09 | 白名单外域名与链路本地地址被阻断，记出站日志 | #28–#31、#47；#71、#73 | `TestMCPEgressWhitelistAndHeaders`、`TestInternalAndMetadataBlocked`、`TestGatewayEgressEndToEnd` | **通过**：脚本第 12 步断言发现接口 HTTP 502 且正文含「出站被拒绝」。D3 已由 #71 修复（放行记录写回 status/耗时）；连接复用时对端 IP 空缺已由 #73 补全。fix-verify 在 #71 后确认放行 status 非 0 | 脚本第 12 步复用演示 `redirector` 思路（`cmd/mockredir`）；并断言放行记录写回状态码（D3 修复前应失败）。D3 为 M1 验收前必须关闭 |
| AC-10 | 预算 80% 告警、100% 拦截并返回可读错误 | #27；#70、#71 | `TestBudgetBlockAlertReconcile`、`TestBudgetAlertStateAndRebuild` | **通过**：脚本第 13 步覆盖 HTTP 拦截与 80%/100% 告警。D2/D6 已由 #70、D4 已由 #71 修复；fix-verify 复跑确认可读预算文案、通知模板按 kind 渲染、错误气泡与输入保留 | 脚本第 13 步覆盖 HTTP 拦截与告警。D2 / D4 / D6 为 M1 验收前必须关闭（修复在 `/workspace/hengdesk-fix-demo`） |
| AC-11 | 审计员按 trace 回放完整链路 | #36 等 | `TestAuditTraceReplay`、`TestAuditDeniedLoginAndQuery` | **通过**：脚本第 14 步按 trace 回放；CI 与界面复跑均通过 | 脚本第 14 步（D 回放 A 的 8–13 步） |
| AC-12 | 审计不可改删（含超管）；失败尝试本身入审计；哈希链可校验 | #8–#11；#68（D5 / D-47）；CI 审计表 append-only | `TestHashChainVerifyAndTamper`、`TestPGTwoProcessConcurrentChainWrite`、CI「审计表 append-only」；应用无删除/修改审计 API | **通过**：D5 / D-47 已随 #68（`bf4d437`）修复。`TestPGTwoProcessConcurrentChainWrite`：两进程合计 200 并发、2000 条写入失败 0、全链校验通过。fix-verify（main `50797cb`）界面「链完整，已校验 300 条」。#69 断网 15 步 `GET /audit-logs/verify` 通过，overview=summary=records=676；超管删除被拒（404 或 405）。须按 D-48 在新初始化库上验收，正式包基于 `bf4d437`+ | 脚本第 14 步：回放、改配置 404、超管删除被拒，并调用 `GET /audit-logs/verify`（D5 修复前应失败，不放宽）。D5 由 `/workspace/hengdesk-audit` 修；本块不改审计写入代码。D-47 并发标准不变 |
| AC-13 | 查看/复制/导出/API 均带显式或元数据标识 | #61、#63、#66 | `TestAIGCSettingsAndExport`、`TestDefaultsValidateSave`、`TestAppendSuffixAndMeta` | **通过**：D-46（#66）已进 main；脚本第 8 步与收尾断言 Markdown front matter、非法文案 400、导出属主；CI 打印「D-46 文案校验与导出属主断言通过」 | 脚本第 8 步与 D-46 断言：后缀、`X-HengDesk-AIGC`、JSON 元数据、**md front matter**；空文案或不含「人工智能生成 / AI 生成」保存失败 |
| AC-14 | 本地 OpenAI 兼容端点 + 国产 API，「测试」可流式对话并记 usage | #25、#26、#30 | `TestModelTestConnection`、`TestChatNonStreamSSEAndACL`（mock）；`TestVendorContract*`（dashscope / vLLM 回放带） | **部分**：脚本第 3 步对 mockupstream「测试」已随断网 15 步通过。通义真实冒烟已按 D-49 移出 M1；契约测试在 CI 中运行，但 dashscope 回放带仍为「按公开文档构造，非真实录制」，尚未按 D-49 换源（未编造替换）。vLLM CPU 真实协议冒烟脚本已就绪，本执行环境无 Docker，未实跑。Ollama 未验证。DeepSeek 仅本机可选 | 脚本第 3 步对 mockupstream 点「测试」。通义改走契约测试 + 标明来源的回放（待补，见 D-49 行）。vLLM CPU 真实冒烟保留：`tests/acceptance/vllm-cpu-smoke.sh`，有 Docker 的机器实跑后补记 `docs/demo/vllm-cpu-smoke.md`。客户现场通义冒烟见 `docs/demo/poc-delivery.md` |
| AC-15 | 任何角色查看配置或 API 都看不到密钥明文 | #25 | `TestModelAdminCRUDAndKeyNeverReturned`、`TestChatUpstream401Redacted` | **通过**：脚本第 3 步创建供应商后 GET 响应无明文 key，断网 15 步 CI 通过 | 脚本第 3 步：创建供应商后 GET 响应无明文 key |
| AC-16 | 看板与 `usage_record` 汇总 token 一致，估算单独标注 | #59、#63 | `TestStatsOverviewScopeExport`、`TestRebuildHours`、`TestSummaryByAgent` | **通过**：脚本第 15 步；fix-verify 复跑记录财务部用量对账 tokens=7090 / calls=18 一致 | 脚本第 15 步：财务部看板 vs 明细汇总 |
| AC-17 | 发布构建许可证扫描不含白名单之外许可 | CI `license scan + SBOM` | 该 job 本身 | **通过**：本块与修复 PR（含 #69、#72）的 license scan + SBOM 均绿，未新增例外 | 本块 PR 继续走该门禁；不新增例外 |
| D-46 安装记录（与 AC-01 同一缺口） | 正式离线包在干净断网机上完整安装并留记录 | #62 提供打包/安装；记录尚未有 | CI 仅有临时 `docker save` 冒烟，不是正式包在干净机上的安装记录 | **未覆盖 / M1 验收前必须补做** | 与真机演示「本机无 Docker，AC-01 未演示」合并为同一验收项，不分开跟踪。PR3 在 CI 断网环境尽量用正式打包流程跑 15 步；PR4 写明本执行环境（无 Docker、非全新服务器）与真正干净断网机的差异，并把「验收时在干净断网机上用正式离线包完整安装并归档记录」列为人工步骤 |
| D-46 卸载 | `--volumes` 须交互确认（非交互加 `--yes`），并提示先备份；`secrets/` 保留 | #62 已有 `-y` / `--volumes`；备份提示与文案可能未齐 | 脚本层手工/本块补测 | **通过**：验收脚本收尾对 `uninstall.sh` 断言（无 `--yes` 时 `--volumes` 拒绝；有确认则删卷、保留 secrets），随断网 15 步 CI 通过 | 验收脚本在收尾对 `uninstall.sh` 做断言（无 `--yes` 时 `--volumes` 拒绝；有确认则删卷、保留 secrets） |
| D-46 导出权限 | 只能导出自己的会话 | #61 `conv_export` 已走 `own()` | 会话属主测试有，导出越权需在脚本中显式断言 | **通过**：脚本断言 B 导出 A 的会话返回 404，随断网 15 步 CI 通过（「D-46 文案校验与导出属主断言通过」） | 脚本：B 导出 A 的会话返回 404 |
| D-48 审计链基线 | 旧哈希不迁移；正式包须基于 `bf4d437`+ 构建；验收在新初始化的库上做；入链规范化不得静默改变，改动须带哈希版本号并按版本校验或迁移 | #68 / `bf4d437` 为方案版本 1 基线 | 文档：`docs/dev/audit.md`、`docs/release/m1.md`、演示手册（#72） | **通过（文档 + 新库验收）**：#72 已写清构建基线与清空重建。#69 断网 15 步与 fix-verify 均在新初始化库上校验通过。本块不改哈希算法、不做旧链迁移 | 手册与发布说明写明构建基线与清空重建；本块不改哈希算法、不做旧链迁移 |
| D-47 审计并发 | server+worker 并发写审计不得误拒；全链可校验 | #68 / `bf4d437` | `TestPGTwoProcessConcurrentChainWrite`（两进程合计 200 并发、2000 条，失败 0、全链校验通过） | **通过**：修复已合入 #68。15 步与界面复跑哈希链均通过。本块未改审计写入代码 |
| D-49 通义替代验收 | 通义真实冒烟移出 M1；改为按官方文档做契约测试，并用录制回放测兼容层。回放样本只能来自 vLLM CPU 真实录制或通义官方文档示例，须标明来源，禁止编造。vLLM CPU 真实协议冒烟保留。POC 交付手册须含：在客户现场用客户自己的密钥做一次通义真实冒烟 | #30 已有 `TestVendorContract*` 与 `testdata/dashscope_cassette.json`；文档 #72 | 契约测试框架已在 CI 中运行；回放带 `_source` 现为「按公开文档构造，非真实录制」 | **部分 / M1 验收前待补**：#72 已把 D-49 写入验收清单、发布说明与 `docs/demo/poc-delivery.md`。回放带尚未按官方文档示例或 vLLM 真实录制换源（未编造替换）。vLLM CPU 冒烟待有 Docker 的机器实跑 | 文档见本表、`docs/demo/m1-acceptance.md`、`docs/demo/poc-delivery.md`、`docs/release/m1.md`。替换回放带如工作量在半天内另开 PR，否则保持本待补项 |

## PR 拆分

| PR | 内容 | 对应 | 状态 |
|---|---|---|---|
| PR1 | 本计划与 AC 对照表 | 本块起步 | 已合并 #65（`2f9771a`，2026-11-09 19:13 America/Los_Angeles） |
| PR2 | `tests/acceptance/`：run.sh、15 步 Go 脚本、testdata、`hengdesk/mocks` 与 `docker-compose.acceptance.yml`；vLLM CPU 协议冒烟脚本；对照表按 D-47 修订 | §13 第 1–15 步，AC-01～AC-16 端到端，D-46 断言，D-47 记录项 | 已合并 #67（`e39fa83`，2026-11-09 20:13 America/Los_Angeles） |
| PR3 | CI job「断网 15 步验收」：`pack-offline.sh --with-acceptance`，断网安装后跑完整 15 步，iptables 断言无公网出站；`run.sh --offline` | AC-01、D-46 安装项的自动化部分 | 已合并 #69（`8268127`，2026-11-09 23:38 America/Los_Angeles） |
| PR4 | `docs/demo/m1-acceptance.md` 演示手册 + 本机模拟干净安装记录（含与真正干净机器的差异）；D-48 / D-49 文档与 POC 交付手册 | 验收人手册 | 已合并 #72（`dec4e1e`，2026-11-09 23:58 America/Los_Angeles） |
| 缺陷修复 | 跑脚本时发现的缺陷就地修，每个缺陷一个小 PR；AC 未覆盖则补实现或测试 | 视发现而定 | 已合并 #68（D5/D-47，`bf4d437`）、#70（D1/D2/D6，`50797cb`）、#71（D3/D4，`db38fba`）、#73（放行日志对端 IP，`560ca84`） |
| 完成报告 | 第八块完成报告（含对照表最终状态、演示方式、待确认、仍需人工补做的项） | — | 本 PR |

## 待确认（先按以下默认值实现，不阻塞开发）

1. **演示 IdP**：PRD 写「Keycloak 演示 IdP，随演示包提供」。M1 验收脚本默认用仓库内轻量 **mock OIDC**（可打进验收离线包，体积小、可断网）。Keycloak 镜像体积大，不打进默认离线包；若产品要求必须用 Keycloak，列为验收时可选手动步骤或 P1 演示附加包。
2. **OIDC 配置入口**：当前只能通过 `HENGDESK_OIDC_*` 环境变量在启动时启用，安装向导里没有图形化配置。脚本与手册默认：在 `deploy/.env` 写入配置并重启 server。运行时管理界面列为 P1。
3. **mocks 是否进入正式离线包**：默认正式包（`pack-offline.sh`）不含 mocks；`pack-offline.sh --with-acceptance`（或独立 `pack-acceptance.sh`）才打入 mocks 镜像、compose 覆盖件和验收脚本。AC-01 的「正式包」安装记录用默认包；15 步剧本用验收包或在正式包安装后再加载 mocks。
4. **市场部只能用本地模型**：脚本按此设置模型 ACL。两个「测试」端点在 CI 中都指向 mockupstream，用不同 provider 名称区分「国产 API」与「本地」；不在 CI 里打真实 DeepSeek / Ollama。
5. **财务部预算 X**：取演示用小值（默认 400 tokens，80% 告警、100% 拦截）。嵌入计入用量但不拦截（D-42），第 8 步与 AC-05 检索后会超过 80，故不用 80，以免第 9 步被提前拦截；第 13 步仍在 40 轮内触达 100%。
6. **AC-05 插入位置**：剧本原文无单独一步，脚本在第 8 步后追加「授予后再问」，不改变第 1–15 步编号。
7. **通义、vLLM、Ollama**（D-49 修订）：通义真实冒烟从 M1 验收项移除。M1 用官方文档契约测试 + 标明来源的录制回放（样本仅限 vLLM 真实录制或通义官方文档示例，禁止编造）；现有「文档构造」dashscope 回放带为 **M1 验收前待补项**。vLLM CPU 真实协议冒烟保留在 M1；本执行环境无 Docker，脚本与记录模板先合入，实跑在有 Docker 的机器补记。客户现场通义冒烟写入 POC 交付手册。Ollama 本机能拉起则顺手验证，否则写入完成报告。

## 真机演示发现的缺陷（M1 验收前必须关闭）

真机演示已于 2026-11-09 19:22–19:30（America/Los_Angeles）在 `/workspace/hengdesk-demo` 对 `main` @ `5e3c970` 跑完 PRD §13 的 15 步界面剧本（无 Docker，改为本机二进制；记录见该工作区 `README.md`）。演示本身不在本块合入范围。下列缺陷**必须在 M1 验收前关闭**；本块**不修改**这些产品代码。

| 编号 | 影响的 AC / 步骤 | 现象 | 修复工作区 | 本块验收脚本态度 |
|---|---|---|---|---|
| **D1** | 第 8 步会话页（前端） | `GET /conversations/:id` 返回嵌套 `{conversation,messages,files}`，前端按扁平字段读，智能体标识/开场白/只读状态错乱 | `/workspace/hengdesk-fix-demo`（迁移 `20261228`–`20261229`） | HTTP 剧本不依赖该前端解析；对照表仍列为必须关闭 |
| **D2** | AC-10 通知 | 所有通知（含审批）都按「预算提醒」模板渲染 | 同上 | 脚本检查 `kind=budget_alert`；UI 模板错误需前端修复 |
| **D3** | AC-09 出站日志 | `egress_logs` append-only 触发器拒绝 `Start` 之后的 UPDATE，放行记录 status 恒为 0、无 IP/耗时 | 同上 | 第 12 步断言放行记录写回状态码，修复前应失败，不放宽 |
| **D4** | AC-10 / 第 9 步自动续写 | 续写被预算拦截时读顶层 `message`，显示「Too Many Requests」而非可读预算提示；且工具在拦截前已执行 | 同上 | 第 13 步断言 HTTP 拦截正文含「预算/用尽/管理员」；续写路径需后端修复 |
| **D5（严重，AC-12）** | AC-12 哈希链 | `usage` 以 struct 入链、jsonb 往返后键序变化，校验在带 usage 的 `model.call` 处报断链 | `/workspace/hengdesk-audit`（与 D-47 同路，迁移自 `20261225`） | 第 14 步调用 `GET /audit-logs/verify` 且要求 `ok=true`，修复前应失败，不放宽。本块不改审计写入代码 |
| **D6** | AC-10 前端提示 | 429 预算拦截后 `finally` 清空实时气泡，用户看不到「预算已用尽」，输入也被丢掉 | `/workspace/hengdesk-fix-demo` | 属 UI；对照表列为必须关闭 |

另外：真机演示因本机无 Docker **未能演示 AC-01**（第 1 步改为二进制部署）。这与下方 D-46「正式离线包在干净、断网机器上完整安装并留记录」是**同一项** M1 验收缺口，见对照表 AC-01 / D-46 安装记录，不重复开单。 D1–D6 已由 #68 / #70 / #71 关闭，#73 关闭了 fix-verify 记录的放行日志 IP 遗留；详见完成报告。

## 阻塞（不在本块修复）

撰写完成报告时，下列原阻塞项的关闭情况如下。原条目保留作过程记录。

- **已关闭**：D-47 / D5（#68）；D1、D2、D6（#70，fix-verify 复跑确认）；D3、D4（#71）；D3 连接复用缺 IP 的遗留（#73）；D-46 增量断言（#66，随 #69 脚本通过）。
- **仍待人工补做**：AC-01 / D-46 正式包干净断网安装记录；D-49 dashscope 回放带换源；vLLM CPU 真实协议冒烟实跑。


- **D-47 审计链多进程写入误拒**（M1 退出阻塞）：修复已合入 #68（`bf4d437`）。验收标准：server+worker 200 并发写入失败 0、全链校验通过（该并发测试在审计工作区，本 job 跑 `GET /audit-logs/verify`）。
- **缺陷 D5（AC-12）**：哈希链校验断链，修复已随 #68 合入。脚本仍断言 `ok=true`。
- **缺陷 D1、D2、D3、D4、D6**：由 `/workspace/hengdesk-fix-demo` 修复（迁移 `20261228`–`20261229`）。均为 M1 验收前必须关闭。本块不改这些代码。
- **D-46 增量已进 main（#66 / `a64fd31`）**：脚本按拍板断言 Markdown front matter、非法 AIGC 文案 400、导出他人会话 404、`uninstall --volumes` 确认。
- **AC-01 / D-46 正式包干净断网安装记录**：真机演示因本机无 Docker 未覆盖 AC-01；须在干净断网机上用正式离线包完整安装并归档，作为 M1 验收人工步骤。
- **D-49 通义回放带来源**：已有契约测试，但 `dashscope_cassette.json` 为「按公开文档构造」，须替换为通义官方文档示例或 vLLM CPU 真实录制，并标明来源，禁止编造。列为 M1 验收前待补项。

## 进度记录

（每个 PR 合并后在这里补一条：PR 号、squash 提交、内容摘要。）

- PR1（#65 / `2f9771a`，2026-11-09 19:13 America/Los_Angeles）：第八块计划与 AC-01～AC-17 / D-46 对照表。
- PR2（#67 / `e39fa83`，2026-11-09 20:13 America/Los_Angeles）：15 步脚本、演示 mock、对照表按 D-47 与真机演示缺陷修订。已基于 D-46（#66）rebase。
- 依赖与修复：D-46 #66（`a64fd31`，2026-11-09 19:45）；审计 D5 / D-47 #68（`bf4d437`，2026-11-09 20:11）；演示缺陷 D1/D2/D6 #70（`50797cb`，2026-11-09 21:02）；D3/D4 #71（`db38fba`，2026-11-09 20:42）；放行日志连接复用补全对端 IP #73（`560ca84`，2026-11-09 21:40）。
- 界面复跑：`/workspace/hengdesk-demo/fix-verify.md` 于 2026-11-09 21:08–21:13 America/Los_Angeles 对 main `50797cb`（含 #70、#71）跑完 15 步界面剧本，退出码 0，耗时 250 秒；哈希链「链完整，已校验 300 条」；密钥自检 0 命中。#73 随后关闭该记录中「连接复用时放行日志 IP 为空」的遗留。
- PR3（#69 / `8268127`，2026-11-09 23:38 America/Los_Angeles）：CI「断网 15 步验收」。合入前提交 `c653350` 的 run [42774002679](https://github.com/SSStq1314/hengdesk/actions/runs/42774002679) 全绿：断网探测通过，15 步全部通过，哈希链校验通过，overview=summary=records=676，D-46 文案与导出属主断言通过，mark 之后未发现 `HD_EGRESS`。产品出站控制（D-47 DNS `local_verify`）未改；CI 层 iptables 不放行公网解析器。
- PR4（#72 / `dec4e1e`，2026-11-09 23:58 America/Los_Angeles）：演示手册、干净安装记录差异、D-48 / D-49 与 POC 交付手册。rebase 到 #69 之后 run [42776879051](https://github.com/SSStq1314/hengdesk/actions/runs/42776879051) 全绿，15 步再次全部通过，mark 之后无 `HD_EGRESS`。
- 修复 #75（`bf4c9ec`，2026-11-10 01:13 America/Los_Angeles）：SQLite 预算用量窗口按进程本地墙钟比较，避免 TZ=Asia/Shanghai 下当地 ≥ 16:00 时 used=0。
- 完成报告：本 PR。


## 第八块完成报告

写这份报告时，main 为 `bf4c9ec`（已含 #69、#72、#75 及下方修复 PR）。本 PR 合入后以合并提交为准。

### PR 与合并提交

| PR | 内容 | squash 提交 | 合入时间（America/Los_Angeles） |
|---|---|---|---|
| #65 | PR1：第八块计划与 AC-01～AC-17 / D-46 对照表 | `2f9771a` | 2026-11-09 19:13 |
| #66 | 第七块后续：D-46 统计 / AIGC / 离线包拍板落地（本块依赖） | `a64fd31` | 2026-11-09 19:45 |
| #67 | PR2：15 步端到端验收脚本、演示 mock、vLLM CPU 冒烟脚本 | `e39fa83` | 2026-11-09 20:13 |
| #68 | 修复 D5 哈希链断裂与 D-47 多进程审计写入误拒 | `bf4d437` | 2026-11-09 20:11 |
| #71 | 修复演示缺陷 D3 出站日志与 D4 续写预算提示 | `db38fba` | 2026-11-09 20:42 |
| #70 | 修复演示缺陷 D1/D2/D6 与智能体工具权限提示 | `50797cb` | 2026-11-09 21:02 |
| #73 | 连接复用时放行日志补全对端 IP（fix-verify 遗留） | `560ca84` | 2026-11-09 21:40 |
| #69 | PR3：断网环境跑完整 15 步验收（CI iptables + 探测后 mark） | `8268127` | 2026-11-09 23:38 |
| #72 | PR4：演示手册、干净安装记录差异、D-48 / D-49、POC 交付手册 | `dec4e1e` | 2026-11-09 23:58 |
| #75 | 修复 SQLite 预算用量窗口在 TZ=Asia/Shanghai 且当地 ≥ 16:00 时漏计（#55 同类，非 Redis） | `bf4c9ec` | 2026-11-10 01:13 |
| 本 PR | 第八块完成报告（更新对照表最终状态） | 见本 PR 合并提交 | — |

以上均在 CI 全绿后 squash 合入，未强合。#72 在 #69 进 main 之后 rebase 再合。

### CI 与 15 步结果

- #69 合入前 head `c653350`，run [42774002679](https://github.com/SSStq1314/hengdesk/actions/runs/42774002679) 13/13 成功。其中「断网 15 步验收」：断网探测通过（验收进程无法访问外网解析器或公网地址）；15 步全部通过；哈希链校验通过；`overview=676 summary=676 records=676`；D-46 文案校验与导出属主断言通过；在探测结束后于宿主机 `mark`（2026-11-09 23:19 America/Los_Angeles），其后内核日志未发现 `HD_EGRESS`。
- #72 rebase 后 head `d8de06a`，run [42776879051](https://github.com/SSStq1314/hengdesk/actions/runs/42776879051) 13/13 成功。15 步再次全部通过，哈希链与 D-46 断言通过，mark 之后无 `HD_EGRESS`。
- 产品出站控制未改（D-47：DNS 默认 `local_verify`）。CI 层 `tests/acceptance/ci/assert-no-egress.sh` 只在 `DOCKER-USER` 上放行已建立连接、RFC1918 / 回环（含 Docker 嵌入 DNS `127.0.0.11`）与相应 IPv6，**不**放行公网解析器的 UDP/TCP 53；验收容器内对 `8.8.8.8` / `1.1.1.1` 与公网域名的解析或连接必须失败。
- `/workspace/hengdesk-demo/fix-verify.md`：2026-11-09 21:08–21:13 America/Los_Angeles 对 main `50797cb`（已含 #70、#71）复跑界面 15 步，退出码 0，耗时 250 秒。D1–D6 关闭；第 14 步界面「链完整，已校验 300 条」。密钥自检文本 44 个文件、截图 OCR 122 张，命中 0。该记录中「连接复用时放行日志 IP 为空」的遗留已由随后合入的 #73 关闭。
- 本执行环境无 Docker，15 步实跑仅在 CI 完成；未在本机跑 vLLM CPU 冒烟或正式包干净安装。
- 本完成报告初稿 CI 的 `go (lint + test)` 曾两次在 `TZ=Asia/Shanghai` 失败（#74 run [42779642770](https://github.com/SSStq1314/hengdesk/actions/runs/42779642770) 及其 rerun）：`TestBudgetBlockAlertReconcile` 等 5 个用例 `used=0` / 未拦截。不是 Redis 偶发（日志里的 `127.0.0.1:45687` 连接拒绝来自 `TestBudgetFailClosed` 故意关闭的 miniredis）。根因与 #55 同类：`DBUsed` 用 `.UTC()` 边界去比 SQLite 进程本地墙钟，上海当地 ≥ 16:00 时把当天用量漏计。已由 #75（`bf4c9ec`）改为按 `time.Local` 比较，并补 16:00 前后单测。生产 PostgreSQL `timestamptz` 按瞬时比较，不受此墙钟问题影响。


### AC-01～AC-17 最终状态（摘要）

| 项 | 最终状态 |
|---|---|
| AC-01 | **部分**：断网 CI 安装冒烟与完整 15 步均通过，mark 后出站为 0。正式离线包在干净断网机上的安装记录仍待人工补做 |
| AC-02 | **通过**（mock OIDC；Keycloak 为可选手动步骤） |
| AC-03 | **通过** |
| AC-04 | **通过** |
| AC-05 | **通过** |
| AC-06 | **通过** |
| AC-07 | **通过** |
| AC-08 | **通过**（compose 真重启后续写） |
| AC-09 | **通过**（#71、#73） |
| AC-10 | **通过**（#70、#71；fix-verify 确认可读文案与前端提示） |
| AC-11 | **通过** |
| AC-12 | **通过**（#68；fix-verify 300 条完整；CI 676 条校验通过） |
| AC-13 | **通过**（#66，D-46 断言随 15 步通过） |
| AC-14 | **部分**：mock「测试」通过；通义真实冒烟已按 D-49 移出 M1。dashscope 回放带已换成官方文档示例（见文末「2026-11-11 收尾」），不宣称真实调用。vLLM CPU 实跑与 Ollama 仍未在本环境完成 |
| AC-15 | **通过** |
| AC-16 | **通过** |
| AC-17 | **通过** |
| D-46 安装记录 | **未覆盖 / 待人工**（与 AC-01 同一缺口） |
| D-46 卸载 / 导出权限 | **通过** |
| D-48 | **通过**（#72 文档；#69 与 fix-verify 均在新库上校验） |
| D-47 | **通过**（#68，200 并发写入失败 0、全链校验通过） |
| D-49 | **部分**：官方文档示例回放带与来源契约测试已落地（见文末）。vLLM CPU 真实协议冒烟仍待实跑。通义真实调用不在本项，客户现场见 POC 手册 |

逐项依据与对应 PR 见上方对照表。

### 演示方式

1. **断网自动化（CI / 验收包）**：在有网机构建 `./deploy/pack-offline.sh --with-acceptance`，将包拷到断网环境后 `./install.sh --offline --up --no-build --pull never`（Compose 项目名 `hengdesk`），再跑 `tests/acceptance/run.sh --offline`。CI 对应 job 为「断网安装冒烟（AC-01）」与「断网 15 步验收」。脚本按 PRD §13 第 1–15 步发 HTTP（cookie 会话 + CSRF），穿插未登录 401、越权 404、审计 fail-closed、审批中重启、D-46 文案/导出/卸载确认。CI 只用 mock 上游，不读取 `DEEPSEEK_API_KEY`。
2. **界面演示**：按 `docs/demo/m1-acceptance.md` 在离线 x86 机器上逐步操作，对照每步预期。合入 #70/#71 后的界面复跑记录见 `/workspace/hengdesk-demo/fix-verify.md`。
3. **vLLM CPU 真实协议冒烟**（保留在 M1，不进断网 CI 门禁）：`tests/acceptance/vllm-cpu-smoke.sh`，完成后补记 `docs/demo/vllm-cpu-smoke.md`。
4. **正式包干净安装**：用默认包（不含 mocks）在干净、断网机器上 `install.sh --offline --up`，按手册归档记录。与真正干净机器的差异已写在演示手册中。
5. **客户现场通义冒烟**：见 `docs/demo/poc-delivery.md`，使用客户自己的密钥，不把密钥带离现场或写入仓库。

### 仍需人工补做的项

1. **正式离线包在干净、断网机器上完整安装并归档记录**（AC-01 / D-46 第 8 条）。本执行环境无 Docker、也不是全新服务器；CI 覆盖了打包流程与断网 15 步，但不能替代该人工记录。
2. **按 D-49 替换 dashscope 回放带**：已在 2026-11-11 收尾中改为通义官方文档示例（见文末）。vLLM / Anthropic 构造回放带仍不是通义证据。
3. **vLLM CPU 真实协议冒烟实跑**：脚本与记录模板已合入，待有 Docker 的机器跑完后写入 `docs/demo/vllm-cpu-smoke.md`。
4. **Ollama**：本执行环境未拉起、未验证。
5. **客户现场用客户自己的密钥做一次通义真实冒烟**：POC 交付手册项，不是仓库离线 CI 门禁。
6. **Keycloak 演示 IdP**：验收默认 mock OIDC；若产品要求 Keycloak，列为可选手动步骤或 P1 演示附加包。
7. **DeepSeek 本机可选联调**：密钥只许出现在环境变量 `DEEPSEEK_API_KEY`，不得写入仓库、CI、`.env` 或日志，只记录通过与否。本块 CI 未跑真实 DeepSeek。

## 2026-11-11 收尾（相对 #74 报告）

完成报告撰写时点名的 main 是 `bf4c9ec`，这个提交就是 #75，SQLite 用量窗口修复已经在里面。当时的 main `3d05dbd` 相对 `bf4c9ec` 新增的是 #74（完成报告）和 #76（PG 上海下午窗口），不是又包含一次 #75。#76 不改变本表里除时区回归以外的验收结论。

| 项 | 状态 |
|---|---|
| D-49 通义回放带 | **官方文档示例已替换编造样本**。来源页：`https://help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope` 与 `https://help.aliyun.com/en/model-studio/qwen-api-via-openai-chat-completions`，获取时间 `2026-11-11T15:48:00Z`。每条 `provenance` 写了章节和非语义变换（SSE 封装、页面已省略的中间 chunk 不补造、正文省略号原样保留、401 取自同一页状态码说明）。`_claim` 为 `official_documentation_example_not_a_live_call`。**不是通义真实调用，也不是 vLLM 实测**。英文页带省略号的示例不是完整实录 |
| 契约测试 | `TestDashScopeCassetteProvenance` 核对来源与官方原文片段；`TestVendorContract*` 用这些响应走现有 dashscope 兼容层。没有官方 JSON 的错误体不编造 |
| vLLM / Anthropic 回放带 | 仍是历史构造样本，不拿来充当通义通过 |
| AC-14 | 仍是 **部分**，直到 vLLM CPU 真实协议冒烟在最终 head 上通过 |
| AC-01 / D-46 安装记录 | D-50 工作流在 `result.txt=pass` 之前保持未验证。含 mocks 的 15 步 CI 不是正式包干净安装 |

## 2026-11-11 D-50 取证（相对 #74 报告）

D-50 允许用全新 GitHub 托管 x86 VM 关闭 D-46 第 8 条，但只证明该云 VM。工作流「D-50 正式包断网安装」在准备阶段只构建**无 mock 默认正式包**，隔离之后才 `docker load`。安装进程在 `hd-sut`，容器转发和 dockerd 新公网连接被拒绝。控制通道若保留，必须证明安装进程和产品用不上。验收包含新库 healthz、未登录 401、部门标记、重启后标记仍在。

job 成功且 artifact `d50-offline-evidence` 里的 `result.txt` 为 `pass` 之前，AC-01 / D-46 安装记录保持**未验证**。成功也只记「该次托管 x86 VM」，不记物理机或客户现场。资源不足导致的失败同样是未验证。含 mocks 的 15 步 CI 仍然不是本项。通义回放不得写成通义真实服务通过。
