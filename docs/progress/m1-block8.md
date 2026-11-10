# M1 第八块：端到端演示脚本与 M1 验收自测（进度与拆分）

依据 PRD §13 M1 退出标准的 15 步端到端演示剧本、§14 AC-01～AC-17，以及 decisions.md 的 D-30～D-47。本块在第一至第七块均已合入 main（当时 `5e3c970`，第七块完成报告 #64）的基础上，把演示剧本做成可重复运行的验收脚本，补齐对照表中尚未被端到端覆盖的断言，并写出给验收人看的演示手册。

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

### 真实上游冒烟（D-47）

- **通义**真实冒烟在 M1 **挂起，M2 W1 补测**。对照表不再把缺通义凭据标成本块合入阻塞。
- **vLLM**改为官方 CPU 镜像（`vllm/vllm-openai-cpu`）+ 约 0.5B 小模型做协议冒烟，加上仓库内既有契约测试（`contract_vendors_test.go` 的 vLLM 回放带）。脚本：`tests/acceptance/vllm-cpu-smoke.sh`。本执行环境无 Docker，未能实跑；记录见 `docs/demo/vllm-cpu-smoke.md`。有 Docker 的机器按脚本跑完后补记结果。该冒烟需拉镜像与权重，**不进入断网 CI 必跑门禁**。
- **Ollama** 仍为待办：本机能拉起则顺手验证，否则写入完成报告。
- **DeepSeek** 规则不变：密钥只在环境变量 `DEEPSEEK_API_KEY`，绝不打印、不写进仓库/CI/`.env`/日志，只记录通过与否。

### 审计链多进程写入（D-47，M1 阻塞）

多进程（server + worker）并发写审计时的误拒是 M1 退出阻塞项，由 `/workspace/hengdesk-audit` 修复，迁移自 `20261225`。**本块不修改审计写入代码**。验收标准：server + worker **200 并发写入失败 0、全链校验通过**。合入该修复前，对照表该行保持阻塞。

## AC-01～AC-17 对照表

状态取值：**通过**（已有自动化且 CI 绿）、**未覆盖**（本块要补端到端或记录）、**部分**（集成已通过，端到端或 D-46 增量未齐）、**阻塞**（缺依赖未合入）、**挂起**（明确延期到后续迭代）。

| AC | 含义 | 对应实现 PR（main） | 已有自动化测试 / 脚本 | 当前状态 | 本块计划 |
|---|---|---|---|---|---|
| AC-01 | 离线安装，出站为 0 | #62 | CI job「断网安装冒烟（AC-01）」；`deploy/pack-offline.sh` / `install.sh --offline` | 部分：CI 冒烟通过；**真机演示因本机无 Docker 未演示 AC-01**（改为二进制部署）。该缺口与 D-46「用正式离线包在干净、断网机器上完整安装并留记录」是同一项验收，必须在有 Docker 的干净断网机上补做并归档 | 15 步脚本第 1 步；PR3 CI 在断网环境跑完整剧本；PR4 在 `docs/demo/` 写清与真正干净机器的差异，并把该安装记录列为 M1 验收人工步骤（与真机演示未覆盖项合并，不另开一条） |
| AC-02 | SSO 首次登录自动建号、归入部门、记审计 | #12 及后续 OIDC（`oidc_test.go` / `oidc_d34_test.go`） | `TestOIDCAutoCreateAndLinkBySub`、`TestOIDCDeptMappingByPath` / `ByCode` | 部分：集成通过；演示包无 Keycloak；OIDC 仅能通过环境变量启用 | 脚本第 2 步：mock OIDC + 部门/人员 CSV；断言建号、部门、审计。Keycloak 见待确认 |
| AC-03 | 未授权智能体列表不可见、详情 404 | #53、#55、#60 | `TestAgentCRUDPublishVisibility`、`TestAgentDeptScope`、`TestD45RuntimeIntersection` | 部分：集成通过 | 脚本第 11 步（市场部 B） |
| AC-04 | 文件级 RAG：无权限者检索/上下文/引用都不含该文件 | #37–#45 | `TestKBRetrieveACL`、`TestChatKBCitations`、`kb/recall` 越权为 0 | 部分：集成通过 | 脚本第 8 步（A 问差旅，不含高管文件） |
| AC-05 | ACL 即时生效，无需重建索引 | 第四块 PR2 | `TestKBRetrieveACL`（自定义文件 ACL 后立即命中） | 部分：集成通过 | 脚本在第 8 步后补：授予 B 高管文件权限后再问一次，能检索到 |
| AC-06 | 需审批工具在通过前上游调用数为 0，通过后恰好 1 次 | #46–#52、#54 | `TestApproveExecutesExactlyOnceWithSnapshot`、`TestApprovalCenterEndToEnd`、`TestD44AutoContinueOnApprove` | 部分：集成通过 | 脚本第 9 步（对照 mockmcp 调用计数） |
| AC-07 | 拒绝或超时后模型收到「已拒绝/已超时」，并记审计 | #49、#54 | `TestRejectWithdrawExpire`、`TestD44AutoContinueOnRejectAndExpire` | 部分：集成通过 | 脚本第 10 步（C 拒绝） |
| AC-08 | 挂起审批时重启 server，通过后对话能继续 | #54（D-44） | `TestD44RestartWhileAwaiting`、`TestD44RestartDuringContinuation`（进程内重启） | 部分：集成通过；未在 compose 真重启上覆盖 | 脚本反例：等待审批时经 docker.sock / `-restart-cmd` 重启 server，再由 C 通过并续写 |
| AC-09 | 白名单外域名与链路本地地址被阻断，记出站日志 | #28–#31、#47 | `TestMCPEgressWhitelistAndHeaders`、`TestInternalAndMetadataBlocked`、`TestGatewayEgressEndToEnd` | 部分：集成与真机演示（用 redirector）通过拦截；**缺陷 D3**：放行日志因 append-only 触发器无法 UPDATE，status 恒为 0 | 脚本第 12 步复用演示 `redirector` 思路（`cmd/mockredir`）；并断言放行记录写回状态码（D3 修复前应失败）。D3 为 M1 验收前必须关闭 |
| AC-10 | 预算 80% 告警、100% 拦截并返回可读错误 | #27 | `TestBudgetBlockAlertReconcile`、`TestBudgetAlertStateAndRebuild` | 部分：真机演示后端拦截与告警通过；**D4** 自动续写被预算拦截时文案不可读且工具已执行；**D6** 前端 429 提示只闪现即被清空；**D2** 通知横幅误用预算模板 | 脚本第 13 步覆盖 HTTP 拦截与告警。D2 / D4 / D6 为 M1 验收前必须关闭（修复在 `/workspace/hengdesk-fix-demo`） |
| AC-11 | 审计员按 trace 回放完整链路 | #36 等 | `TestAuditTraceReplay`、`TestAuditDeniedLoginAndQuery` | 部分：集成通过 | 脚本第 14 步（D 回放 A 的 8–13 步） |
| AC-12 | 审计不可改删（含超管）；失败尝试本身入审计；哈希链可校验 | #8–#11；CI 审计表 append-only | `TestHashChainVerifyAndTamper`、CI「审计表 append-only」；应用无删除/修改审计 API | **阻塞**：真机演示越权/不可改删通过，但「校验哈希链」失败（**缺陷 D5**，usage 规范化断链）。另：多进程并发写入误拒（D-47）仍为 M1 阻塞 | 脚本第 14 步：回放、改配置 404、超管删除被拒，并调用 `GET /audit-logs/verify`（D5 修复前应失败，不放宽）。D5 由 `/workspace/hengdesk-audit` 修；本块不改审计写入代码。D-47 并发标准不变 |
| AC-13 | 查看/复制/导出/API 均带显式或元数据标识 | #61、#63 | `TestAIGCSettingsAndExport`、`TestDefaultsValidateSave`、`TestAppendSuffixAndMeta` | 部分：集成通过；D-46 要求更严的文案校验与 Markdown front matter 尚未按拍板落地 | 脚本第 8 步与 D-46 断言：后缀、`X-HengDesk-AIGC`、JSON 元数据、**md front matter**；空文案或不含「人工智能生成 / AI 生成」保存失败 |
| AC-14 | 本地 OpenAI 兼容端点 + 国产 API，「测试」可流式对话并记 usage | #25、#26 | `TestModelTestConnection`、`TestChatNonStreamSSEAndACL`（mock）；`TestVendorContract*`（dashscope / vLLM 回放带，文档构造） | 部分：mock 与契约回放通过；通义真实冒烟挂起；vLLM CPU 协议冒烟脚本已就绪、本环境未实跑；Ollama 未验证；DeepSeek 仅本机可选 | 脚本第 3 步对 mockupstream 点「测试」。通义：**挂起，M2 W1 补测**。vLLM：`tests/acceptance/vllm-cpu-smoke.sh` + 既有契约测试，有 Docker 的机器实跑后补记 `docs/demo/vllm-cpu-smoke.md` |
| AC-15 | 任何角色查看配置或 API 都看不到密钥明文 | #25 | `TestModelAdminCRUDAndKeyNeverReturned`、`TestChatUpstream401Redacted` | 部分：集成通过 | 脚本第 3 步：创建供应商后 GET 响应无明文 key |
| AC-16 | 看板与 `usage_record` 汇总 token 一致，估算单独标注 | #59、#63 | `TestStatsOverviewScopeExport`、`TestRebuildHours`、`TestSummaryByAgent` | 部分：集成通过 | 脚本第 15 步：财务部看板 vs 明细汇总 |
| AC-17 | 发布构建许可证扫描不含白名单之外许可 | CI `license scan + SBOM` | 该 job 本身 | 通过（每个 PR 门禁） | 本块 PR 继续走该门禁；不新增例外 |
| D-46 安装记录（与 AC-01 同一缺口） | 正式离线包在干净断网机上完整安装并留记录 | #62 提供打包/安装；记录尚未有 | CI 仅有临时 `docker save` 冒烟，不是正式包在干净机上的安装记录 | **未覆盖 / M1 验收前必须补做** | 与真机演示「本机无 Docker，AC-01 未演示」合并为同一验收项，不分开跟踪。PR3 在 CI 断网环境尽量用正式打包流程跑 15 步；PR4 写明本执行环境（无 Docker、非全新服务器）与真正干净断网机的差异，并把「验收时在干净断网机上用正式离线包完整安装并归档记录」列为人工步骤 |
| D-46 卸载 | `--volumes` 须交互确认（非交互加 `--yes`），并提示先备份；`secrets/` 保留 | #62 已有 `-y` / `--volumes`；备份提示与文案可能未齐 | 脚本层手工/本块补测 | 部分 | 验收脚本在收尾对 `uninstall.sh` 做断言（无 `--yes` 时 `--volumes` 拒绝；有确认则删卷、保留 secrets） |
| D-46 导出权限 | 只能导出自己的会话 | #61 `conv_export` 已走 `own()` | 会话属主测试有，导出越权需在脚本中显式断言 | 部分 | 脚本：B 导出 A 的会话返回 404 |
| D-47 审计并发 | server+worker 并发写审计不得误拒；全链可校验 | 修复在 `/workspace/hengdesk-audit`（迁移自 20261225），尚未合入 | 待该工作区提供 200 并发测试 | **阻塞（M1 退出）** | 本块**不改审计写入代码**。验收标准：server+worker 200 并发写入失败 0、全链校验通过。合入修复后在完成报告勾选 |

## PR 拆分

| PR | 内容 | 对应 | 状态 |
|---|---|---|---|
| PR1 | 本计划与 AC 对照表 | 本块起步 | 已合并 #65（`2f9771a`，2026-11-09 19:13 America/Los_Angeles） |
| PR2 | `tests/acceptance/`：run.sh、15 步 Go 脚本、testdata、`hengdesk/mocks` 与 `docker-compose.acceptance.yml`；vLLM CPU 协议冒烟脚本；对照表按 D-47 修订 | §13 第 1–15 步，AC-01～AC-16 端到端，D-46 断言，D-47 记录项 | 已合并 #67（`e39fa83`，2026-11-09 20:13 America/Los_Angeles） |
| PR3 | CI job「断网 15 步验收」：`pack-offline.sh --with-acceptance`，断网安装后跑完整 15 步，iptables 断言无公网出站；`run.sh --offline` | AC-01、D-46 安装项的自动化部分 | 进行中 |
| PR4 | `docs/demo/m1-acceptance.md` 演示手册 + 本机模拟干净安装记录（含与真正干净机器的差异） | 验收人手册 | 待开始 |
| 缺陷修复 | 跑脚本时发现的缺陷就地修，每个缺陷一个小 PR；AC 未覆盖则补实现或测试 | 视发现而定 | 按需 |
| 完成报告 | 第八块完成报告（含对照表最终状态、演示方式、待确认、阻塞） | — | 最后 |

## 待确认（先按以下默认值实现，不阻塞开发）

1. **演示 IdP**：PRD 写「Keycloak 演示 IdP，随演示包提供」。M1 验收脚本默认用仓库内轻量 **mock OIDC**（可打进验收离线包，体积小、可断网）。Keycloak 镜像体积大，不打进默认离线包；若产品要求必须用 Keycloak，列为验收时可选手动步骤或 P1 演示附加包。
2. **OIDC 配置入口**：当前只能通过 `HENGDESK_OIDC_*` 环境变量在启动时启用，安装向导里没有图形化配置。脚本与手册默认：在 `deploy/.env` 写入配置并重启 server。运行时管理界面列为 P1。
3. **mocks 是否进入正式离线包**：默认正式包（`pack-offline.sh`）不含 mocks；`pack-offline.sh --with-acceptance`（或独立 `pack-acceptance.sh`）才打入 mocks 镜像、compose 覆盖件和验收脚本。AC-01 的「正式包」安装记录用默认包；15 步剧本用验收包或在正式包安装后再加载 mocks。
4. **市场部只能用本地模型**：脚本按此设置模型 ACL。两个「测试」端点在 CI 中都指向 mockupstream，用不同 provider 名称区分「国产 API」与「本地」；不在 CI 里打真实 DeepSeek / Ollama。
5. **财务部预算 X**：取演示用小值（默认 400 tokens，80% 告警、100% 拦截）。嵌入计入用量但不拦截（D-42），第 8 步与 AC-05 检索后会超过 80，故不用 80，以免第 9 步被提前拦截；第 13 步仍在 40 轮内触达 100%。
6. **AC-05 插入位置**：剧本原文无单独一步，脚本在第 8 步后追加「授予后再问」，不改变第 1–15 步编号。
7. **通义、vLLM、Ollama**（D-47 修订）：通义真实冒烟 **挂起，M2 W1 补测**，不挡本块合入。vLLM 用官方 CPU 镜像 + 0.5B 级小模型做协议冒烟，加上既有契约测试；本执行环境无 Docker，脚本与记录模板先合入，实跑在有 Docker 的机器补记。Ollama 本机能拉起则顺手验证，否则写入完成报告。

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

另外：真机演示因本机无 Docker **未能演示 AC-01**（第 1 步改为二进制部署）。这与下方 D-46「正式离线包在干净、断网机器上完整安装并留记录」是**同一项** M1 验收缺口，见对照表 AC-01 / D-46 安装记录，不重复开单。

## 阻塞（不在本块修复）

- **D-47 审计链多进程写入误拒**（M1 退出阻塞）：修复已合入 #68（`bf4d437`）。验收标准：server+worker 200 并发写入失败 0、全链校验通过（该并发测试在审计工作区，本 job 跑 `GET /audit-logs/verify`）。
- **缺陷 D5（AC-12）**：哈希链校验断链，修复已随 #68 合入。脚本仍断言 `ok=true`。
- **缺陷 D1、D2、D3、D4、D6**：由 `/workspace/hengdesk-fix-demo` 修复（迁移 `20261228`–`20261229`）。均为 M1 验收前必须关闭。本块不改这些代码。
- **D-46 增量已进 main（#66 / `a64fd31`）**：脚本按拍板断言 Markdown front matter、非法 AIGC 文案 400、导出他人会话 404、`uninstall --volumes` 确认。
- **AC-01 / D-46 正式包干净断网安装记录**：真机演示因本机无 Docker 未覆盖 AC-01；须在干净断网机上用正式离线包完整安装并归档，作为 M1 验收人工步骤。

## 进度记录

（每个 PR 合并后在这里补一条：PR 号、squash 提交、内容摘要。）

- PR1（#65 / `2f9771a`）：第八块计划与 AC-01～AC-17 / D-46 对照表。
- PR2（#67 / `e39fa83`）：15 步脚本、演示 mock、对照表按 D-47 与真机演示缺陷修订。已基于 D-46（#66）rebase。
- 依赖进展：D-46 #66、审计/D5 #68 已进 main。演示缺陷 D1–D4、D6 的修复工作区尚未开 PR，D3 仍可能让第 12 步（放行日志状态码）失败；合入后 rebase 复跑，不放宽断言。
