# D-36 主密钥（产品经理定）

> 同步自 decisions.md（产品经理，2026-11-03）。原文如下，实现说明附后。

- 主密钥文件为 deploy/secrets/master.key，算法 AES-256-GCM。
- 下一个 PR 做以下几项：
  1. **安装向导**：首次安装结束时，明确显示主密钥路径，提示「丢失后已存的 Key 无法恢复，请与数据库备份分开保管」，管理员须手动确认才能继续。在后台「系统状态」中，主密钥未确认备份时常驻提示。
  2. **轮换命令**：`hengdesk keys rotate-master`。在一个事务内用新密钥重新加密全部密文，失败则整体回滚；旧密钥文件保留为 .bak，由管理员自行删除。轮换操作记审计。
  3. **启动自检**：主密钥缺失或与库内密文不匹配时，服务明确报错并给出处理指引，不能静默启动、事后才在调用时报错。
- 备份策略：/backups 默认不包含主密钥；备份完成时提示单独保管主密钥。

## 实现说明（M1 第二块 PR2）
- 第 1 点：`install.sh` 结束时打印主密钥路径与警示，交互模式须输入 `yes`；非交互模式须带 `--ack-master-key`，否则安装失败。
  后台「系统状态」接口返回 `master_key`（是否配置、路径、指纹、是否已确认）；未确认时管理后台常驻红色提示，
  点「我已单独备份」调用 `POST /api/v1/admin/system/master-key/ack`（记审计，写失败则拒绝）。确认绑定主密钥指纹，轮换后需重新确认。
- 第 2 点：`hengdesk keys rotate-master --yes`（停服后经 `docker compose --profile tools run --rm keytool keys rotate-master --yes` 执行）。
  事务内解密 → 用新密钥重新加密全部上游 Key → 写审计 `model.master_key.rotate`，任一步失败整体回滚、密钥文件不变；
  成功后旧文件保留为 `master.key.bak`，保持原属主与 600 权限。`upgrade.sh` 会把新密钥同步进 `.env`。
- 第 3 点：server 启动时自检，主密钥缺失但库内有密文、或无法解密时启动失败，错误信息给出处理指引；
  `hengdesk keys check` 可单独检查。
- 备份：`/backups` 不包含主密钥（CI 校验），备份完成日志提示单独保管主密钥。运维步骤见 [model-gateway.md](../ops/model-gateway.md#主密钥)。
