# 预算与配额运维说明

## 时区
周期（每日 / 每月）按服务进程的本地时区计算，请在部署时设置 `TZ`（如 `TZ=Asia/Shanghai`）。

## Valkey 与对账
- 实时计数在 Valkey，键 `budget:used:*`，TTL 15 分钟；`usage_records` 是权威数据。
- 计数丢失（Valkey 重启、清库）会在下一次调用时自动从 `usage_records` 重建。
- 手动对账：管理页「预算与配额」→「对账并重建计数」，或
  `POST /api/v1/admin/budgets/reconcile`（可加 `?id=<预算 ID>`）。返回每条预算的计数值、库内值与偏差，并按库内值重置。
- **Valkey 不可用时 fail-closed**：存在适用预算的调用返回 503 `budget_unavailable`；管理页用量列提示「计数服务不可用」。

## 错误码
| HTTP | code | 含义 |
|------|------|------|
| 429 | `budget_exceeded` | 超出预算，`error.budget` 给出预算对象、已用、上限、重置时间 |
| 503 | `budget_unavailable` | 计数服务不可用，按 fail-closed 拒绝 |

## 审计动作
`budget.create`、`budget.update`、`budget.delete`、`budget.reconcile`、`budget.alert`；被拦截的调用记为 `model.call` denied。
