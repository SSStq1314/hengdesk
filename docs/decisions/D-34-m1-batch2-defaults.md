# D-34 M1 第二批默认值（产品经理定）

> 同步自 decisions.md（产品经理，2026-11-02，回应 docs/progress/m1-block1.md）。原文如下，实现说明附后。

1. **OIDC 自动建号（JIT）**：默认开启，但只有配置了邮箱域名白名单才会真正建号；白名单为空就等于关闭（安全的默认值）。新建账号只给最低的「普通成员」角色，部门按 claim 映射（FR-03/AC-02，属于 P0）。超级管理员和审计员不按邮箱自动合并，只能手动绑定，绑定这个操作要记审计。
   - 「强制 SSO」模式：开启后只有指定的紧急管理员账号（最多 2 个）能用本地密码登录。紧急账号每次登录都记一条高风险审计，并在后台通知审计员。
2. **HTTP 端口**：80 端口可以配置，HTTP 跳转 HTTPS 也可以关掉。默认值不变：开启 HTTPS 时占用 80 并跳转。
3. **自签证书**：install.sh 增加 `--self-signed`（可选参数 `--san` 指定域名/IP），有效期 365 天，默认不生成。后台常驻提示「当前为自签证书」，到期前 30 天开始提醒。
4. **审计保留期（FR-40）**：默认 365 天，可以配置，但最低 180 天，不能设得更短。到期前 30 天提醒审计员；由审计员手动触发归档，导出带签名的归档包，包里含段末哈希，同时附带离线校验命令。在线表移除之前，先在链上写一条「归档断点」审计。系统永远不自动删除。

## 实现说明
- 第 1 点（OIDC）：PR #21 落实，配置与接口见 [docs/dev/oidc.md](../dev/oidc.md)。
  - `HENGDESK_OIDC_AUTO_CREATE` 默认改为 `true`；`HENGDESK_OIDC_EMAIL_DOMAINS` 为空时一律不建号。自动建号只给「成员」角色。
  - 超级管理员 / 安全审计员按邮箱登录时拒绝（审计 reason=`privileged_link_refused`）；管理员用 `PUT /api/v1/admin/users/:id/oidc-identity` 手动绑定，记 `auth.oidc.bind` / `auth.oidc.unbind` 审计。
  - 强制 SSO：`HENGDESK_OIDC_ENFORCE=true` 时须配置 1～2 个 `HENGDESK_OIDC_BREAKGLASS_EMAILS`，超过 2 个启动失败。紧急账号每次密码登录写 `alert.high_risk`（severity=high）审计；审计员后台显示未读提醒（`GET /api/v1/admin/alerts`）。邮件 / IM 通知通过 `auth.Notifier` 接口扩展，暂未接入。
- 第 2 点（HTTP 端口）：PR #22 落实，见 [docs/ops/tls.md](../ops/tls.md)。
- 第 3～4 点（自签证书、审计保留期）另行安排。
