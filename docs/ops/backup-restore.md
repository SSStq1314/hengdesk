# 升级、备份与恢复

衡台的数据库迁移**只向前执行**，不提供 down 迁移。回滚的唯一方式是用升级前的备份恢复（D-30、D-31）。

## 自动备份
- `hengdesk migrate` 发现有待执行的迁移，且数据库已有数据时，会先用 `pg_dump --format=custom` 备份到 `HENGDESK_BACKUP_DIR`（compose 中是 `backups` 卷，挂载在 `/backups`）。
- 备份成功后只保留最近 `HENGDESK_BACKUP_KEEP` 份（默认 5），更早的自动删除。备份盘剩余空间低于 `HENGDESK_DISK_LOW_PERCENT`（默认 10%）时，管理后台显示常驻告警（server 以只读方式挂载 `backups` 卷读取剩余空间）。
- 备份先写成 `*.partial`，经 `pg_restore --list` 校验可读后才改为正式文件名 `hengdesk-<UTC 时间>-pre-upgrade.dump`。
- **备份失败时直接中止升级，数据库不做任何改动**，服务仍可用旧版本启动。
- 全新安装（空库）不备份。
- M0 预览版（由 ent 自动建表、没有迁移记录）的库不支持原地升级。migrate 会报错退出，需要备份后重新安装。
- 数据库版本比程序新时（即降级运行），以及已执行的迁移文件内容被改动时，migrate 都会拒绝执行。

## 从 Debian pgvector 换到自有 Alpine 镜像

`pgvector/pgvector:0.8.7-pg17` 的数据目录由 Debian glibc 初始化。`hengdesk/postgres` 是 Alpine。两套 libc 和 locale 不能共用同一个数据目录，升级脚本发现没有 `hengdesk-image` 标记时：

1. 只用旧镜像把库 `pg_dump -Fc` 到 `/backups/hengdesk-pre-alpine-pgdata.dump`。
2. 把原目录复制到卷 `hengdesk_pgdata_debian_kept`。
3. 清空原卷，让新镜像在空目录上初始化，再 `pg_restore`。
4. 导入失败时，把保留的旧目录拷回。不要用 `hengdesk/postgres` 打开这份旧目录。

回退是这份 dump 的 `pg_restore`，或者在导入完成前使用保留的旧目录和旧镜像。没有向下的 schema migration。`HENGDESK_PG_MIGRATE_FAIL=after-dump` 会在清空目录之前停下，用来演练失败。

## 升级
```bash
cd deploy
# 1. 把 .env 中 HENGDESK_VERSION 改为目标版本（离线包先 docker load 新镜像；源码部署用 --build）
./upgrade.sh            # 或 ./upgrade.sh --build
```
脚本的执行顺序：
1. 停止 server / worker。
2. `docker compose run --rm migrate`，即自动备份加迁移。
3. `docker compose up -d`。
4. 列出最近的备份。

## 恢复（回滚）
```bash
cd deploy
docker compose exec postgres ls -1t /backups     # 找到升级前的备份
# 如需回退版本：先把 .env 的 HENGDESK_VERSION 改回升级前的版本
./restore.sh hengdesk-20261102T120000Z-pre-upgrade.dump
```
`restore.sh` 的执行顺序：
1. 校验备份可读。
2. 二次确认（`-y` 可跳过）。
3. 停止 web / server / worker。
4. `DROP DATABASE ... WITH (FORCE)`，然后重建空库。
5. `pg_restore --exit-on-error`。
6. 不重新执行迁移，直接启动服务。

> 恢复会删除当前数据库的全部数据，包括升级后产生的审计记录。如需保留，恢复前先执行 `docker compose exec postgres pg_dump -Fc -U hengdesk hengdesk > before-restore.dump`。

## 把备份拷出主机
```bash
docker compose cp postgres:/backups/<文件名> ./
```
对象存储（SeaweedFS / MinIO）和 Valkey 的数据不在 pg_dump 范围内：
- 对象存储：备份其数据卷，或使用对象存储自身的复制能力。
- Valkey：只存会话和队列，恢复后用户需要重新登录。

## CI 验证
compose 冒烟（seaweedfs）会执行一次完整链路：安装 → 写入标记数据 → 注入一个探针迁移并升级（自动备份）→ 用备份恢复 → 确认探针表消失且标记数据仍在。
