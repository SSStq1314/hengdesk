# D-42 知识库检索性能定稿与 M1 第四块默认值（产品经理定）

> 同步自 `decisions.md` 第 393–421 行（产品经理，2026-11-04，补充修订 D-27）。下面是原文，未作改写；实现说明在后面。

依据：docs/progress/d27-retest.md，测试规模 100 万条 × 1024 维，测试机 8C16G，索引加数据共 13 GB，超出内存。

**性能目标改为按硬件档和可见比例分别定**
- **推荐硬件**：100 万条以上至少 32 GB 内存，并用 pg_prewarm 预热。达到推荐硬件时，在热缓存下：
  - **低可见比例**（走精确检索那一档）：向量段 P95 ≤100 ms，召回率 1.0。
  - **高可见比例**（含 50% 可见，走 HNSW）：recall@10 ≥0.95，这条**必须在「50% 可见 + 热缓存」下达到**；向量段 P95 ≤200 ms。
  - 端到端检索 P95 ≤500 ms，与原目标不变。
- **冷缓存**不设考核目标，但服务启动后自动预热。
- **低于推荐硬件**时不承诺上述指标。当索引大小超过内存的 50% 时，后台提示管理员。
- 越权命中必须为 0，这是硬性门槛，适用于所有档位。

**M1 要做的事**：ef_search 默认值改为 400，索引改用 halfvec，做完后在 32 GB 机器上复测。这项**不阻塞**后面的块，但必须在 M1 验收前完成并达标。「可见比例高时按知识库过滤」作为优化项：如果实现成本低就放进 M1，否则列为 P1。

**受信代理（D-41 实现口径）**：确认只看 TCP 直连对端地址、不看 X-Forwarded-For。多层代理支持列为 P2。

**M1 第四块默认值（m1-block4.md）**
照原样采纳：
- 超级管理员不会自动获得知识库权限。补充：超级管理员给自己授权时，记高风险审计。
- embedding 只记用量，不受预算拦截。仍然计入用量和告警。
- 不检查 embedding 模型的使用权限，因为 embedding 模型由管理员按知识库配置。
- 不能撤销最后一个 manage 权限，返回 409。
- 设了自定义权限的文件，只有被单独授权的人和知识库的 manage 持有者能看到。
- 历史消息里的引用按当前权限过滤，没有权限的完全隐藏。

修改后采纳：
- **检索失败**：仍返回 503，但界面要说明「知识库暂时不可用」，并提供「重试」按钮，以及「本次不使用知识库继续」按钮。
- **没检索到资料**：默认提示用户，并允许模型用通用知识回答，但回答要标注「未引用知识库资料」。另外新增按知识库配置的「仅依据资料回答」开关，开启后没有资料时直接告知用户、不作答，放进 M1。
- **检索查询**：M1 只用最新一条用户消息检索。「结合上下文改写追问」列为 P1。

## 实现说明（工程）

| 条目 | 实现 | 测试 |
|---|---|---|
| ef_search 400 | `kb.DefaultEfSearch` 100 → 400（HNSW 档 `SET LOCAL hnsw.ef_search`，iterative_scan=relaxed_order 不变）。 | `kb/recall_pg_test.go` HNSW 档用默认值 |
| halfvec 索引 | 迁移 `20261120000000_m1_kb_d42.sql`：删除 `kbchunk_embedding_hnsw`，新建表达式索引 `kbchunk_embedding_hnsw_half ON kb_chunks USING hnsw ((embedding::halfvec(1024)) halfvec_cosine_ops)`（m=16、ef_construction=64）。列仍是 float32：精确档不变；HNSW 档内层按半精度距离走索引取候选，外层按 float32 距离重排并返回相似度。升级时重建索引（100 万条本机约 2.5 分钟，期间 HNSW 档走顺序扫描，结果仍正确）。 | `kb/recall_pg_test.go`：50% 可见 recall@10 ≥ 0.95（真值为 float32 暴力结果）、越权 0、计划使用 halfvec 索引、旧索引已删除 |
| 启动预热 | `server` 启动后后台执行 `kb.Prewarm`：`CREATE EXTENSION IF NOT EXISTS pg_prewarm`，依次预热向量索引、`kbchunk_file_id_seq`、`kb_chunks`。失败只记 warn，不影响启动。`kbbench` 默认同样预热（`-prewarm=false` 可关）。 | `kb/recall_pg_test.go`（尽力而为调用） |
| 索引超内存 50% 提示 | `GET /admin/system/status` 新增 `kb_index{index_bytes, memory_bytes, over_half}`；内存取 `HENGDESK_DB_MEMORY_MB`（数据库分机部署时填写），未配置时读本机 `/proc/meminfo`。超过一半时管理后台常驻告警条「知识库向量索引已超过内存的一半」。 | `kb/recall_pg_test.go`：`CheckIndex` 大 / 小内存两种情况 |
| 可见比例高时按知识库过滤 | **列为 P1**，见下文。 | — |
| 复测 | 见 `docs/progress/d27-retest.md`「D-42 复测」。**32 GB 机器上的复测待做，是 M1 验收的前置条件。** | `cmd/kbbench`（新增 `-reindex`、`-prewarm`，输出 recall@10） |
| 受信代理 | 未改动（D-41 实现即只看 TCP 对端）；多层代理列 P2。 | — |
| 超管给自己授权 | 知识库与文件授权（`kb.acl.grant` / `kb.file.acl.grant`）时，若操作者是超级管理员且授权对象覆盖本人（本人、本人所在部门、包含本人所在部门的上级部门且含下级、本人持有的角色），在 attempt 审计之后写高风险审计 `alert.high_risk`（kind=`kb_self_grant_by_super_admin`，带资源与授权内容），写失败拒绝执行、权限不变。撤销不告警。 | `api/kb_d42_test.go`：`TestKBSuperAdminSelfGrantAlert` |
| embedding 用量与告警 | 原实现只写 usage_record、**不触发预算结算，告警不会发出**。现 `kb.Retriever` 与 `kb.Indexer` 新增 `Settle` 钩子，在用量落库后调用 `budget.Service.Settle`（API 内联索引、worker 索引、会话检索三处都已接上）；仍不做预检拦截。 | `api/kb_d42_test.go`：`TestKBEmbeddingBudgetAlert`（动作为「拦截」的模型预算下，索引与检索照常执行且各触发一次 `budget.alert`） |
| 检索失败 / 未检索到 / 仅依据资料回答 | 见第二个 PR（对话侧）。 | — |
| 结合上下文改写追问 | 列 P1；M1 只用最新一条用户消息检索（现状）。 | — |

### 「可见比例高时按知识库过滤」列为 P1 的原因

1. **正确性风险高于收益**：按知识库过滤要把「可见文件列表」换成「知识库 ID + 排除列表」（文件自定义权限、非 ready 文件、删除中的文件都要排除）。这要求排除列表与检索在同一快照下计算，否则授权变更与检索之间存在越权窗口；越权命中为 0 是硬性门槛，M1 不值得为此冒险。
2. **改动不小**：需要改 `Retriever` 的可见集合计算、两条检索 SQL、kb_id 索引与计划检查、`kbbench` 与召回测试，以及 MemStore 实现。
3. **收益未量化**：本次复测中 50% 可见档的向量阶段 P95 为 57 ms（目标 ≤ 200 ms），端到端 P95 169 ms（目标 ≤ 500 ms），延迟不是瓶颈；瓶颈是召回率，按知识库过滤不能提升召回。
