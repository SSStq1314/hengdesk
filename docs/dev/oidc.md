# OIDC 单点登录（M1）

基于 `github.com/coreos/go-oidc/v3`（Apache-2.0）与 `golang.org/x/oauth2`（BSD-3-Clause），流程为 authorization code + PKCE（S256）。SAML / LDAP 为 P1，本期不做。

## 配置（环境变量）
| 变量 | 默认 | 说明 |
|---|---|---|
| `HENGDESK_OIDC_ISSUER` | 空 | 为空即不启用 OIDC |
| `HENGDESK_OIDC_CLIENT_ID` / `HENGDESK_OIDC_CLIENT_SECRET` | 空 | IdP 注册的客户端 |
| `HENGDESK_OIDC_REDIRECT_URL` | 空 | 必须为 `https://<域名>/api/v1/auth/oidc/callback`，并在 IdP 登记 |
| `HENGDESK_OIDC_SCOPES` | `openid email profile` | |
| `HENGDESK_OIDC_LINK_BY` | `email` | `email`：先按 (issuer, sub)，再按 **email_verified=true** 的邮箱关联未绑定 SSO 的本地账号；`sub`：只认 (issuer, sub) |
| `HENGDESK_OIDC_AUTO_CREATE` | `true`（D-34） | 无匹配账号时自动建号（只给成员角色、默认组织）。**仅当邮箱域名在白名单内才建号** |
| `HENGDESK_OIDC_EMAIL_DOMAINS` | 空 | 邮箱域名白名单（逗号分隔，不区分大小写）。为空时一律不自动建号；非空时按邮箱关联也须命中 |
| `HENGDESK_OIDC_DEPT_CLAIM` | 空 | 部门 claim 名（字符串或字符串数组）；ID Token 里没有时再查 UserInfo。为空不映射 |
| `HENGDESK_OIDC_DEPT_MATCH` | `path` | `path`：按部门名路径匹配（如 `研发部/后端组`）；`code`：按部门编码（部门的 external_id）匹配 |
| `HENGDESK_OIDC_DEPT_DEFAULT` | 空 | 全部映射不到时归入的部门（写法同 `DEPT_MATCH`）；为空或找不到时保持原部门 |
| `HENGDESK_OIDC_ENFORCE` | `false` | 强制 SSO：开启后只有紧急管理员能用密码登录，其他人密码正确也返回统一 401（审计 reason=`sso_enforced`，不计入锁定） |
| `HENGDESK_OIDC_BREAKGLASS_EMAILS` | 空 | 紧急管理员邮箱，最多 2 个（超过则启动失败）；`ENFORCE=true` 时至少 1 个 |
| `HENGDESK_OIDC_DISPLAY_NAME` | `企业单点登录` | 登录页按钮文案 |

生产环境必须启用 HTTPS（D-30）。

## 接口
- `GET /api/v1/auth/oidc/config`：`{enabled, display_name, enforced}`，登录页据此显示按钮。
- `PUT /api/v1/admin/users/:id/oidc-identity` `{issuer?, subject}`：管理员手动绑定 OIDC 身份（issuer 默认取配置），`DELETE` 同路径解绑。需要成员管理权限，目标为特权账号时还需特权角色授予权限；身份已被他人占用返回 409。
- `GET /api/v1/admin/alerts`、`POST /api/v1/admin/alerts/read {up_to_id}`：审计员的未读高风险事件（站内通知），需审计查看权限。
- `GET /api/v1/auth/oidc/login`：生成 state / nonce / code_verifier（存 Valkey，10 分钟，一次性），state 同时写入 `hd_oidc_state` cookie，302 到 IdP。
- `GET /api/v1/auth/oidc/callback`：校验 state（cookie 与 Valkey 双重、一次性）、换取 token（带 code_verifier）、校验 ID Token 签名 / aud / 过期 / nonce，关联本地用户后签发服务端会话，302 到 `/`；任何失败统一 302 到 `/sign-in?error=sso`，不暴露原因。

## 审计
| action | 场景 |
|---|---|
| `auth.login`（detail.method=oidc） | 成功 / 拒绝（reason：state_mismatch、state_expired、code_exchange、id_token_invalid、nonce_mismatch、no_local_account、disabled、ip_rate_limited、idp_error、email_domain_not_allowed、privileged_link_refused） |
| `auth.oidc.link` | 首次按邮箱关联（特权账号不会发生，见下） |
| `auth.oidc.bind` / `auth.oidc.unbind` | 管理员手动绑定 / 解绑（detail 含 issuer、subject、是否特权账号） |
| `user.department.sync` | 登录时部门有变化（detail：from、to、unmapped、default_used） |
| `alert.high_risk`（detail.severity=high） | 强制 SSO 下紧急管理员密码登录，审计员后台显示未读提醒 |
| `user.create`（detail.via=oidc_auto_create） | 自动建号 |
| `auth.logout` | 登出（本地 / SSO 会话均记） |

全部 fail-closed：审计写入失败则不签发会话、不关联、不建号。

## 安全约束
- 已关联其他 sub 的本地账号不会被改绑；邮箱未验证不做邮箱关联也不建号。
- 超级管理员、安全审计员不按邮箱自动合并：拒绝登录并记审计，只能由管理员手动绑定（D-34）。
- 部门同步只改「成员」角色的部门归属，部门管理员等其他角色的部门范围不受 IdP 影响。
- 高风险事件的外部通知通过 `auth.Notifier`（`Service.Notifiers`）扩展，失败只记日志；站内通知以审计记录为准，不会丢。
- `LINK_BY=sub` 时同邮箱本地账号存在则不建号，避免账号合并。
- 回调同样受按 IP 登录限速约束；已停用账号拒绝登录。

## 测试
`server/internal/auth/oidctest` 是自写的模拟 IdP（discovery、JWKS、authorize、token，校验 PKCE S256 与 client 凭据）。`server/internal/api/oidc_test.go` 用真实 HTTP 服务、cookie jar 跑完整浏览器流程：邮箱关联 → 登录 → /auth/me → 登出审计 → 按 sub 再登录；未开通、邮箱未验证、nonce 错误、缺 state cookie 的拒绝；`LINK_BY=sub` + 自动建号；默认未启用。`oidc_d34_test.go` 覆盖：部门按路径 / 编码映射、UserInfo 兜底、默认部门、变化才记审计；域名白名单（为空不建号）；特权账号拒绝合并与手动绑定 / 解绑；强制 SSO、紧急登录高风险审计、审计员未读提醒与已读；外部通知扩展点。
