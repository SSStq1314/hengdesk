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
| `HENGDESK_OIDC_AUTO_CREATE` | `false` | 无匹配账号时是否自动建号（成员角色、默认组织） |
| `HENGDESK_OIDC_DISPLAY_NAME` | `企业单点登录` | 登录页按钮文案 |

生产环境必须启用 HTTPS（D-30）。

## 接口
- `GET /api/v1/auth/oidc/config`：`{enabled, display_name}`，登录页据此显示按钮。
- `GET /api/v1/auth/oidc/login`：生成 state / nonce / code_verifier（存 Valkey，10 分钟，一次性），state 同时写入 `hd_oidc_state` cookie，302 到 IdP。
- `GET /api/v1/auth/oidc/callback`：校验 state（cookie 与 Valkey 双重、一次性）、换取 token（带 code_verifier）、校验 ID Token 签名 / aud / 过期 / nonce，关联本地用户后签发服务端会话，302 到 `/`；任何失败统一 302 到 `/sign-in?error=sso`，不暴露原因。

## 审计
| action | 场景 |
|---|---|
| `auth.login`（detail.method=oidc） | 成功 / 拒绝（reason：state_mismatch、state_expired、code_exchange、id_token_invalid、nonce_mismatch、no_local_account、disabled、ip_rate_limited、idp_error） |
| `auth.oidc.link` | 首次按邮箱关联 |
| `user.create`（detail.via=oidc_auto_create） | 自动建号 |
| `auth.logout` | 登出（本地 / SSO 会话均记） |

全部 fail-closed：审计写入失败则不签发会话、不关联、不建号。

## 安全约束
- 已关联其他 sub 的本地账号不会被改绑；邮箱未验证不做邮箱关联也不建号。
- `LINK_BY=sub` 时同邮箱本地账号存在则不建号，避免账号合并。
- 回调同样受按 IP 登录限速约束；已停用账号拒绝登录。

## 测试
`server/internal/auth/oidctest` 是自写的模拟 IdP（discovery、JWKS、authorize、token，校验 PKCE S256 与 client 凭据）。`server/internal/api/oidc_test.go` 用真实 HTTP 服务、cookie jar 跑完整浏览器流程：邮箱关联 → 登录 → /auth/me → 登出审计 → 按 sub 再登录；未开通、邮箱未验证、nonce 错误、缺 state cookie 的拒绝；`LINK_BY=sub` + 自动建号；默认未启用。
