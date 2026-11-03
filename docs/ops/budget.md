# 预算与配额运维说明

## 时区
周期（每日 / 每月）按**系统时区**切分（D-37）：环境变量 `HENGDESK_TIMEZONE`，IANA 名（如 `Asia/Shanghai`、`UTC`），
默认 `Asia/Shanghai`。程序内置时区数据，不读取容器的 `TZ`；填写无效时区服务启动失败。修改时区后，当期计数键会随周期起点变化而自动重建。

## 告警去重
发送状态存数据库表 `budget_alert_states`（预算 + 周期 + 档位唯一），Valkey 重启、计数重建都不会重复提醒。
修改预算后立即重建计数并按新档位重新判断：档位回落会清除更高档位的状态，之后再次越过时重新提醒。

## 测试连接与用量未知
- 模型「测试连接」计入用量与预算计数，但不会被超额拦截。
- 调用失败且上游未返回用量时按 0 记账，记录标记「用量未知」（`usage_unknown`），对账时请留意这类记录。

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
