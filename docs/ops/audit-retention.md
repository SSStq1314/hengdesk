# 审计保留期与归档（FR-40，D-34 第 4 点）

## 配置
| 变量 | 默认 | 说明 |
|---|---|---|
| `HENGDESK_AUDIT_RETENTION_DAYS` | `365` | 在线保留天数。低于 180（或高于 36500）时 server / worker / CLI 启动即失败。 |
| `HENGDESK_AUDIT_ARCHIVE_SIGN_KEY_FILE` | 空 | 可选 Ed25519 私钥文件（`hengdesk audit keygen --out` 生成）。配置后归档包带 `manifest.sig`。 |

系统**永远不自动删除**审计记录：到期只提醒，归档必须由安全审计员手动执行。

## 提醒
- worker 每 12 小时检查一次（asynq 定时任务 `audit:retention_remind`）。最早一条在线记录距超期不足 30 天时，写一条 `alert.high_risk`（kind=`audit_retention_due`）审计。审计员后台顶部的站内提醒（复用 #21）显示到期日与剩余天数；同一批记录 7 天内只提醒一次。
- 接口：`GET /api/v1/admin/audit-logs/retention`（需 `audit.read`）返回保留期状态与最近 20 次归档断点。
- 命令行：`hengdesk audit status [--remind]`。

## 归档
```sh
docker compose exec server hengdesk audit archive --auditor auditor@example.com          # 预览
docker compose exec server hengdesk audit archive --auditor auditor@example.com --yes    # 执行
```
`--auditor` 必须是启用状态、持有 `audit.export` 的安全审计员。步骤如下，任何一步失败都立即停止、不删除任何数据（fail-closed）：
1. 选出 `ts` 早于「现在 − 保留期」的最老一段连续记录，重新校验其哈希链，并确认后面一条记录正好接在段末哈希之后。
2. 生成归档包 `audit-archives/<archive_id>.tar.gz` 上传对象存储，再读回比对 sha256。
3. 在链上写一条「归档断点」审计（action=`audit.archive.checkpoint`），记录归档号、对象键、包 sha256、首尾 id 与首尾哈希、条数。
4. 在一个事务内删除这一段记录。数据库触发器只放行「会话变量指向一条已入链的检查点、且被删行 id 落在该检查点记录的范围内」的 DELETE；删除行数与预期不符就整体回滚。

如果在第 2、3 步之后失败，可以直接重跑：多出来的检查点无害，数据仍在线上。删除后 `GET /admin/audit-logs/verify` 仍为 `ok`：剩余链的第一条由最新检查点锚定（返回 `anchor_id`）。

## 归档包与离线校验
包内文件：`records.jsonl`（每行一条原始记录，含 hash / prev_hash）、`manifest.json`（格式 `hengdesk-audit-archive/v1`、首尾 id、首尾哈希、条数）、`SHA256SUMS`、`VERIFY.md`，以及可选的 `manifest.sig`。记录中的 `upstream_trace_id`（D-41）只在非空时出现并参与哈希，旧记录哈希格式不变。

```sh
hengdesk audit fetch-archive audit-archives/<archive_id>.tar.gz --out a.tar.gz   # 从对象存储取回
mkdir x && tar -xzf a.tar.gz -C x && (cd x && sha256sum -c SHA256SUMS)          # 只校验文件完整性
hengdesk audit verify-archive a.tar.gz [--pubkey <hex 公钥>]                    # 完整校验（不连库、不联网）
```
`verify-archive` 会校验 SHA256SUMS、逐条重算哈希并检查链连续、核对 manifest 中的首尾哈希与条数；给了公钥时还校验签名。最后再和数据库中对应检查点的 `last_hash` 比对，即可确认归档段与在线链首尾相接。
