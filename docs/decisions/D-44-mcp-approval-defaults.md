# D-44 MCP 与审批默认值（产品经理定）

> 同步自 `ai-office-platform/decisions.md` 第 431–443 行（产品经理，2026-11-04，PR #46–#52，依据 docs/progress/m1-block5.md「待确认」1–20）。下面是原文，未作改写；实现说明在后面。

**照原样采纳**：2、3、4、5、6、7、8、9、12、14、17、18、19。

**有补充或修改的**
- 1. **stdio MCP 保持 P1**，按 M1 的 P1 顺序排在 License 之后，不提前，控制范围。
- 10. **高风险工具一律不能设为自动执行**，超级管理员也不能放开。这是产品的核心承诺。
- 11. 同意「通过即执行」。执行前除了重新按策略判定，还要再确认**发起人此刻仍有该工具的使用权**，没有就不执行，并把原因作为工具结果返回。
- 13. **改**：审批人为空时，给本组织所有拥有「工具管理」权限的管理员发站内提醒。否则 30 分钟内可能没人发现，审批就超时了。
- 15. **改为方案 (a)，即自动续写**，这是 PRD 原定的体验：审批有结果后，worker 在后台把结果交回模型，自动生成后续回复，并给发起人发站内提醒。「继续」按钮保留，只在自动续写失败时作为兜底。放进下一个 PR，属于 M1 范围。
- 16. 同意等待审批期间锁定会话。发起人可以撤回审批单来解锁；撤回后按第 15 条自动续写，告诉模型「已撤回」。
- 17. 「上限可在组织设置中配置」列为 P1。
- 20. 角色授权界面列为 P1。
- **AC-08（服务重启后能恢复）**：必须补上重启恢复测试，包括等待审批中重启、续写中重启两种情况，M1 验收前完成。

## 实现说明（工程）

| 条目 | 实现 | 测试 |
|---|---|---|
| 10 高危永不自动 | `policy.ValidateSettings` 保存时拒绝 high_risk+auto；`policy.Decide` 历史脏数据按需审批；超管同样拒绝 | `TestD44HighRiskNeverAutoEvenSuperAdmin` |
| 11 执行前复核权限 | `approval.Service.Access`（`authz.ToolAccess`）：通过后执行前再判发起人 use；无权限不执行，原因作工具结果，审计 `tool.call` outcome=denied | `TestD44RecheckACLBeforeExec` |
| 13 空审批人提醒管理员 | `Create` 时审批人为空则 `Managers` 列出拥有 `tool.manage` 的在职用户并发站内提醒 | `TestD44NotifyManagersWhenNoApprovers` |
| 15 自动续写 | 审批有结果 → `OnResolved` 投递 asynq `approval:continue`；worker `Continuer` 以发起人身份复用 continue 逻辑；失败标记 `auto_continue_failed`，前端才显示「继续」；定时 `approval:continue_sweep` 补投 | `TestD44AutoContinueOnApprove` / `OnRejectAndExpire` / `ContinueBusyAndFailure` |
| 16 锁定与撤回续写 | 等待审批时发消息 / 重新生成 409；撤回后 `resolved` 触发自动续写，工具结果为「已撤回」 | `TestD44WithdrawUnlocksAndContinues` |
| AC-08 重启恢复 | 工具循环检查点写入 `tool_state`；执行中崩溃的审批 `RecoverStuck` 按结果未知落库不重试；streaming 孤儿消息退回 awaiting 再续写 | `TestD44RestartWhileAwaiting` / `TestD44RestartDuringContinuation` |
| 17 / 20 P1 | 工具调用步数上限可配、角色授权 UI：**仅文档记录，本 PR 不实现** | — |

其余照原样采纳的条目（2–9、12、14、17–19 中的默认实现）已在第五块 PR1–PR5 落地；本 PR 补齐 D-44 对 10/11/13/15/16/AC-08 的补充与修改。
