#!/usr/bin/env bash
# 衡台升级脚本：停写 → migrate（自动 pg_dump 备份，备份失败即中止，数据库不变）→ 启动新版本。
# 用法：./upgrade.sh [--build]
#   先把 .env 中 HENGDESK_VERSION 改为目标版本（离线包：先 docker load 新镜像）；源码部署加 --build。
# 迁移只向前执行；回滚 = 用本次备份执行 ./restore.sh（见 docs/ops/backup-restore.md）。
set -euo pipefail
cd "$(dirname "$0")"
[ -f .env ] || { echo "缺少 .env，请先运行 install.sh" >&2; exit 1; }
if [ "${1:-}" = "--build" ]; then docker compose build migrate; fi
echo "==> 停止 server / worker（停写，保证备份一致）"
docker compose stop server worker
echo "==> 执行迁移（有待执行迁移时先自动备份）"
if ! docker compose run --rm migrate; then
  echo "升级失败：若为备份失败，数据库未做任何改动。可执行 docker compose up -d 继续使用当前版本。" >&2
  exit 1
fi
echo "==> 启动服务"
docker compose up -d
echo "==> 现有备份（最新在前）："
docker compose exec -T postgres sh -c 'ls -1t /backups | head -5'
