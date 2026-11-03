#!/usr/bin/env bash
# 衡台恢复脚本：用 migrate 生成的 pg_dump 备份整体覆盖数据库（回滚的唯一方式）。
# 用法：./restore.sh <备份文件名> [-y]
#   查看备份：docker compose exec postgres ls -1t /backups
# 注意：恢复会删除当前数据库的全部数据。若同时回退版本，先把 .env 的 HENGDESK_VERSION 改回升级前版本。
set -euo pipefail
cd "$(dirname "$0")"
F="${1:?用法: ./restore.sh <备份文件名> [-y]}"
case "$F" in */*|..*) echo "只接受 /backups 下的文件名" >&2; exit 1 ;; esac
set -a; . ./.env; set +a
U="${POSTGRES_USER:-hengdesk}"; D="${POSTGRES_DB:-hengdesk}"
docker compose exec -T postgres test -s "/backups/$F" || { echo "备份不存在或为空：$F" >&2; exit 1; }
docker compose exec -T postgres pg_restore --list "/backups/$F" >/dev/null || { echo "备份文件损坏：$F" >&2; exit 1; }
if [ "${2:-}" != "-y" ]; then
  read -r -p "将用 $F 覆盖数据库 $D，当前数据会被删除。输入 yes 继续：" ans
  [ "$ans" = yes ] || { echo "已取消"; exit 1; }
fi
echo "==> 停止 web / server / worker"
docker compose stop web server worker
echo "==> 重建数据库并恢复"
docker compose exec -T postgres psql -U "$U" -d postgres -v ON_ERROR_STOP=1 \
  -c "DROP DATABASE IF EXISTS \"$D\" WITH (FORCE)" -c "CREATE DATABASE \"$D\" OWNER \"$U\""
docker compose exec -T postgres pg_restore -U "$U" -d "$D" --no-owner --exit-on-error "/backups/$F"
echo "==> 启动服务（不重新执行迁移）"
docker compose up -d --no-deps server worker web
echo "恢复完成。注意：如果 .env 仍是新版本，下次 docker compose up 会再次升级（并再次自动备份）。"
