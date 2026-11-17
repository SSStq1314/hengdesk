#!/usr/bin/env bash
# 旧的 pgvector/pgvector（Debian）数据目录不能直接交给 hengdesk/postgres（Alpine）。
# libc 和 locale 不同，强行启动会拒绝或损坏数据。这里只做逻辑导出，再在空目录上初始化新库并导入。
# 回退使用这份 dump，或在导入完成前保留的原目录副本。不做反向 schema migration。
# 最后一行只打印 ok 或 need-restore，供 upgrade.sh 读取。
set -euo pipefail
cd "$(dirname "$0")"
if [ "${1:-}" = "--rollback" ]; then
  VOL="${HENGDESK_PG_VOLUME:-hengdesk_pgdata}"
  KEEP="${HENGDESK_PG_KEEP_VOLUME:-hengdesk_pgdata_debian_kept}"
  OLD_IMAGE="${HENGDESK_OLD_PG_IMAGE:-pgvector/pgvector:0.8.7-pg17}"
  BK="${HENGDESK_BACKUP_VOLUME:-hengdesk_backups}"
  docker compose stop postgres >/dev/null 2>&1 || true
  docker run --rm --entrypoint sh -v "$KEEP":/from:ro -v "$VOL":/to "$OLD_IMAGE" -c \
    'find /to -mindepth 1 -delete; cp -a /from/. /to/'
  docker run --rm --entrypoint sh -v "$BK":/backups "$OLD_IMAGE" -c 'rm -f /backups/hengdesk-pg-migrate-pending'
  echo "已把 Debian 数据目录拷回 $VOL。请用旧镜像启动，不要用 hengdesk/postgres 打开它。" >&2
  exit 0
fi
[ -f .env ] || { echo "缺少 .env" >&2; exit 1; }
set -a; . ./.env; set +a
U="${POSTGRES_USER:-hengdesk}"
D="${POSTGRES_DB:-hengdesk}"
PW="${POSTGRES_PASSWORD:?}"
OLD_IMAGE="${HENGDESK_OLD_PG_IMAGE:-pgvector/pgvector:0.8.7-pg17}"
VOL="${HENGDESK_PG_VOLUME:-hengdesk_pgdata}"
KEEP="${HENGDESK_PG_KEEP_VOLUME:-hengdesk_pgdata_debian_kept}"
BK="${HENGDESK_BACKUP_VOLUME:-hengdesk_backups}"
DUMP_NAME="hengdesk-pre-alpine-pgdata.dump"
FAIL_AFTER="${HENGDESK_PG_MIGRATE_FAIL:-}"

say() { printf '%s\n' "$*" >&2; }

if ! docker volume inspect "$VOL" >/dev/null 2>&1; then
  say "没有数据卷 $VOL，按新库初始化"
  printf 'ok\n'
  exit 0
fi
docker volume create "$BK" >/dev/null
runner=""
for candidate in "hengdesk/postgres:${HENGDESK_VERSION:-dev}" "$OLD_IMAGE"; do
  if docker image inspect "$candidate" >/dev/null 2>&1; then runner=$candidate; break; fi
done
[ -n "$runner" ] || { say "没有可用于检查数据目录的镜像"; exit 1; }
probe=$(docker run --rm --entrypoint sh -v "$VOL":/data:ro "$runner" -c '
  if [ -f /data/hengdesk-image ]; then echo marker
  elif [ -f /data/PG_VERSION ]; then echo legacy
  else echo empty; fi')
pending=0
if docker run --rm --entrypoint sh -v "$BK":/backups:ro "$runner" -c 'test -s /backups/hengdesk-pg-migrate-pending'; then
  pending=1
fi

case "$probe" in
  marker)
    say "数据目录已由 hengdesk/postgres 初始化，不迁移"
    printf 'ok\n'
    exit 0
    ;;
  empty)
    if [ "$pending" = 1 ]; then
      say "数据目录已清空，继续导入之前的 dump"
      printf 'need-restore\n'
      exit 0
    fi
    say "空数据目录，交给新镜像初始化"
    printf 'ok\n'
    exit 0
    ;;
  legacy) ;;
  *)
    say "无法识别数据目录：$probe"
    exit 1
    ;;
esac

say "检测到旧的 PostgreSQL 数据目录。不在 Alpine 镜像上启动它。"
docker image inspect "$OLD_IMAGE" >/dev/null 2>&1 || { say "本机没有旧镜像 $OLD_IMAGE，无法导出"; exit 1; }
docker compose stop postgres >/dev/null 2>&1 || true

name="hd-pg-dump-$$"
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
docker run -d --name "$name" \
  -e POSTGRES_USER="$U" -e POSTGRES_PASSWORD="$PW" -e POSTGRES_DB="$D" \
  -v "$VOL":/var/lib/postgresql/data \
  -v "$BK":/backups \
  "$OLD_IMAGE" >/dev/null
ready=0
for _ in $(seq 1 60); do
  if docker exec "$name" pg_isready -U "$U" >/dev/null 2>&1; then ready=1; break; fi
  sleep 2
done
[ "$ready" = 1 ] || { say "旧库未就绪"; docker logs "$name" >&2 || true; exit 1; }
docker exec "$name" pg_dump -U "$U" -d "$D" -Fc -f "/backups/$DUMP_NAME"
docker exec "$name" pg_restore --list "/backups/$DUMP_NAME" >/dev/null
say "已导出 /backups/$DUMP_NAME"
cleanup
trap - EXIT

if [ "$FAIL_AFTER" = after-dump ]; then
  say "按演练要求在清空数据目录之前停止。旧目录没有改动。"
  exit 86
fi

docker volume create "$KEEP" >/dev/null
docker run --rm --entrypoint sh \
  -v "$VOL":/from:ro -v "$KEEP":/to \
  "$OLD_IMAGE" -c 'rm -rf /to/* /to/.[!.]* /to/..?* 2>/dev/null || true; cp -a /from/. /to/'
docker run --rm --entrypoint sh -v "$BK":/backups "$OLD_IMAGE" -c 'printf pending > /backups/hengdesk-pg-migrate-pending'
docker run --rm --entrypoint sh -v "$VOL":/data "$OLD_IMAGE" -c 'find /data -mindepth 1 -delete'
say "原数据目录已复制到卷 $KEEP 并清空。下一步由新镜像初始化后再导入。"
printf 'need-restore\n'
