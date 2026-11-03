# 登录安全（D-33）

| 配置 | 默认 | 说明 |
|---|---|---|
| `HENGDESK_SESSION_IDLE_TIMEOUT` | `2h` | 空闲超时 |
| `HENGDESK_SESSION_TTL` | `12h` | 绝对有效期，到期须重新登录 |
| `HENGDESK_LOGIN_MAX_FAILURES` / `HENGDESK_LOGIN_LOCKOUT_DURATION` | `5` / `15m` | 按账号锁定（0 关闭） |
| `HENGDESK_LOGIN_IP_LIMIT` / `HENGDESK_LOGIN_IP_WINDOW` | `30` / `10m` | 按来源 IP 滑动窗口限速（0 关闭） |
| `HENGDESK_TRUSTED_PROXIES` | 回环 + 私有网段 | 只有对端在这些网段内才读取 `X-Forwarded-For`；`none` 表示从不读取 |
| `HENGDESK_TRACE_TRUSTED_PROXIES` | 空（不接受） | 只有 TCP 对端在这些 IP / CIDR 内时才接受 W3C `traceparent`，另存为审计 `upstream_trace_id`（D-41） |
| `HENGDESK_PASSWORD_MIN_LENGTH` | `8` | 范围 8–72 |
| `HENGDESK_BACKUP_KEEP` | `5` | 保留最近 N 份升级前备份 |
| `HENGDESK_DISK_LOW_PERCENT` | `10` | 备份盘剩余空间低于该百分比时后台告警 |

## 登录响应

| 情况 | 状态码 | 提示 |
|---|---|---|
| 账号不存在 / 密码错误 / 账号已锁定 / 已停用 | 401 | 邮箱或密码错误，或账号暂不可用 |
| 同一来源 IP 超过限速 | 429 + `Retry-After` | 登录尝试过于频繁，请稍后再试 |

未知账号同样执行一次 bcrypt，响应时间与真实账号一致。每次失败都写 `auth.login` 审计（detail.reason：`bad_credentials` / `locked` / `ip_rate_limited`），不记录密码。

管理员解锁：`POST /api/v1/admin/users/:id/unlock`，写 `auth.unlock` 审计。

## 管理后台常驻提示

`GET /api/v1/admin/system/status`（需 `system.settings`）返回 `auditor_assigned`、`disk_free_percent`、`disk_low`、`https`，前端据此显示「尚未指派安全审计员」「备份盘空间不足」等提示。

## 初始安全审计员（D-33）
- `install.sh --auditor-email E [--auditor-password P]`（交互模式也会询问）；留空密码时随机生成并写入 `.env`（权限 600），只提示查看方式，不回显。
- `hengdesk migrate` 读取 `HENGDESK_AUDITOR_EMAIL` / `HENGDESK_AUDITOR_PASSWORD`，幂等创建；邮箱已存在则不做修改。
- 先写 `role.assign`（actor=`system:install`）审计再落库，审计失败则 migrate 失败（fail-closed）。
- 审计员与超级管理员不能是同一账号。
