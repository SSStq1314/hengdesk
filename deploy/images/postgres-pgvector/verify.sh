#!/usr/bin/env bash
# 验证自有 postgres 镜像：官方入口仍能通过重编的 gosu 降权、建库、加载 vector，并转发 SIGTERM。
# 不把「进程能起来」当成全部证据。
set -euo pipefail

img="${1:?用法: verify.sh IMAGE}"
name="hd-pgvec-$$"
pass="ci-only-password-123"

cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "1. gosu 把命令降到 postgres 用户，而不是 root"
uid=$(docker run --rm --entrypoint gosu "$img" postgres id -u)
if [ "$uid" = "0" ] || [ -z "$uid" ]; then
  echo "降权失败，uid=$uid" >&2
  exit 1
fi
echo "postgres uid=$uid"

echo "2. 官方入口完成 initdb 后，数据目录属主是 postgres，并能创建 vector 0.8.7"
docker run -d --name "$name" -e POSTGRES_PASSWORD="$pass" "$img" >/dev/null
ready=0
for _ in $(seq 1 60); do
  if docker exec "$name" pg_isready -U postgres >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done
if [ "$ready" != 1 ]; then
  docker logs "$name" >&2 || true
  echo "postgres 未就绪" >&2
  exit 1
fi
owner=$(docker exec "$name" stat -c '%U' /var/lib/postgresql/data)
if [ "$owner" != "postgres" ]; then
  echo "数据目录属主是 $owner" >&2
  exit 1
fi
mode=$(docker exec "$name" stat -c '%a' /var/lib/postgresql/data)
echo "PGDATA owner=$owner mode=$mode"
docker exec "$name" gosu postgres sh -c 'touch "$PGDATA/hd-perm-test" && stat -c %U "$PGDATA/hd-perm-test"' | grep -qx postgres
ver=$(docker exec "$name" psql -U postgres -v ON_ERROR_STOP=1 -tAc "CREATE EXTENSION vector; SELECT extversion FROM pg_extension WHERE extname='vector';")
echo "$ver" | grep -qx '0.8.7'

echo "3. SIGTERM 交给 postgres，而不是停在 gosu"
docker kill --signal=TERM "$name" >/dev/null
code=$(docker wait "$name")
logs=$(docker logs "$name" 2>&1 || true)
if ! printf '%s\n' "$logs" | grep -E -q 'fast shutdown|shutting down'; then
  echo "日志里没有 postgres 关闭记录，exit=$code" >&2
  printf '%s\n' "$logs" | tail -40 >&2
  exit 1
fi
if [ "$code" != "0" ]; then
  echo "优雅退出码不是 0，而是 $code" >&2
  exit 1
fi
echo "signal_forward_exit=0"
echo "verify_ok"
