# D-45 智能体运行时默认值（产品经理定）

> 同步自 `ai-office-platform/decisions.md` 第 445–457 行（产品经理，2026-11-10，main 50b3ab8，依据 docs/progress/m1-block6.md「待确认」）。下面是原文，未作改写；实现说明在后面。

**照原样采纳**：1（M1 只做对话内同步运行，异步长任务列为 P1）、3、6、7。

**有补充或修改的**
- 2. 只有拥有 manage 权限的人可以发布。补充：每次发布生成一个不可变的版本快照，并记审计；可以回滚到历史版本。
- 4. 同意绑定时操作人必须有使用权。**更关键的是运行时**：智能体运行时，工具、知识库和模型一律按**调用人自己的权限**与智能体绑定范围取交集，不能借智能体越权。
  - 调用人没有权限的工具，对模型不可见。
  - 知识库检索按调用人的 ACL 过滤。
  - 审批单的发起人记为调用人。
  - 「智能体以自身身份授权」（委托授权）列为 P2。
  - 如果当前实现不是这样，请作为 M1 阻塞项修复，并补越权测试。
- 5. 同意每个智能体可配置 1–10 步，默认 5。高风险工具仍然一律走审批（D-44 第 10 条）。
- 8. 删除是软删除。**M1 增加「停用」开关**（可逆，用于出问题时快速止损），只有 manage 权限的人可以操作，并记审计。智能体停用或删除后，已有会话改为只读，不能继续对话。正式的「下架 / 上架」流程列为 P1。

## 实现说明（工程）

| 条目 | 实现 | 测试 |
|---|---|---|
| 1 / 3 / 6 / 7 照原样 | 第六块既有实现：同步对话、未发布仅 edit 可见、部门共享与超管无隐式 ACL | 既有 `TestAgent*` |
| 2 发布快照 + 回滚 | 发布写 `agent_versions` 不可变快照并审计 `agent.publish`；`POST .../versions/:v/rollback`（manage）把 `published_version` 指回历史快照并同步草稿，审计 `agent.rollback` | `TestD45PublishRollbackDisable` |
| 4 运行时权限交集 | `resolveAgent` → `filterAgentTools` / `filterAgentKBs`；`work.ToolIds/KBIds` 只含交集；`convTools` 再按调用人 Usable 组装模型 tools；`Retrieve` 按调用人 ACL；审批 `RequesterID=调用人` | `TestD45RuntimeIntersection`（含工具不可见、KB 剔除、审批发起人）；既有 `TestAgentConversationRuntime` |
| 5 步数与高风险 | 步数 1–10 默认 5；`policy.Decide` 高风险永不 auto，智能体路径复用 | `TestD45DefaultMaxSteps` / `TestD45HighRiskAlwaysApproval` |
| 8 停用 | `agents.disabled`；`POST .../disable|enable`（manage+审计）；广场过滤停用；已有会话 `agent_readonly`，发消息/续写 409 | `TestD45PublishRollbackDisable` |
| P1 正式下架/上架 | **仅文档记录，本 PR 不实现** | — |
| P2 智能体以自身身份授权 | **仅文档记录，本 PR 不实现** | — |
