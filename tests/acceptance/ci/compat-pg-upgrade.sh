#!/usr/bin/env bash
# 用 main 的正式离线包装出 Debian pgvector 数据，再升级到本分支的自有 Alpine 镜像。
# 先演练“导出后失败”：旧目录不动，旧镜像仍能读到标记。再做真正的 dump/restore。
# 回退只恢复备份或拷回保留的旧目录，不把 Alpine 数据目录交回 Debian。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
OLD_TAR="${OLD_TAR:-/tmp/old-pack/hengdesk-offline-main-prev-amd64.tar.gz}"
NEW_TAR="${NEW_TAR:-/tmp/new-pack/hengdesk-offline-compat-new-amd64.tar.gz}"
EVID="${EVID:-/tmp/compat-evidence}"
mkdir -p "$EVID"
test -f "$OLD_TAR"
test -f "$NEW_TAR"

extract() {
  local tar="$1" dest="$2"
  rm -rf "$dest"
  mkdir -p "$dest"
  tar -C "$dest" -xzf "$tar"
  find "$dest" -mindepth 1 -maxdepth 1 -type d | head -1
}

old_root=$(extract "$OLD_TAR" /tmp/compat-old)
new_root=$(extract "$NEW_TAR" /tmp/compat-new)
old_deploy="$old_root/deploy"
new_deploy="$new_root/deploy"

echo "==> 安装 main 正式包"
(
  cd "$old_deploy"
  ./install.sh --non-interactive --ack-master-key --offline --version main-prev \
    --storage seaweedfs --admin-password ci-only-password-123 --force --up
)
for i in $(seq 1 90); do curl -fsS localhost:8080/healthz && break; sleep 2; done
curl -fsS localhost:8080/healthz
docker compose -f "$old_deploy/docker-compose.yml" exec -T postgres \
  psql -U hengdesk -d hengdesk -v ON_ERROR_STOP=1 \
  -c "CREATE TABLE compat_marker (id int); INSERT INTO compat_marker VALUES (7);"

echo "==> 演练：导出之后失败，旧目录保持原样"
docker compose -f "$old_deploy/docker-compose.yml" stop
cp "$old_deploy/.env" "$new_deploy/.env"
chmod 600 "$new_deploy/.env"
set +e
( cd "$new_deploy" && HENGDESK_PG_MIGRATE_FAIL=after-dump ./pg-volume-migrate.sh )
rc=$?
set -e
if [ "$rc" -ne 86 ]; then
  echo "失败演练应返回 86，实际 $rc" >&2
  exit 1
fi
docker compose -f "$old_deploy/docker-compose.yml" up -d postgres
for i in $(seq 1 60); do
  docker compose -f "$old_deploy/docker-compose.yml" exec -T postgres pg_isready -U hengdesk && break
  sleep 2
done
got=$(docker compose -f "$old_deploy/docker-compose.yml" exec -T postgres \
  psql -U hengdesk -d hengdesk -tAc "SELECT id FROM compat_marker" | tr -d '[:space:]')
[ "$got" = 7 ]
echo "失败演练后旧库标记仍是 7" | tee "$EVID/fail-drill.txt"
docker compose -f "$old_deploy/docker-compose.yml" stop

echo "==> 正式升级：逻辑导入到自有 Alpine 镜像"
(
  cd "$new_deploy"
  sed -i 's/^HENGDESK_VERSION=.*/HENGDESK_VERSION=compat-new/' .env
  ./upgrade.sh
)
for i in $(seq 1 90); do curl -fsS localhost:8080/healthz && break; sleep 2; done
curl -fsS localhost:8080/healthz
(
  cd "$new_deploy"
  ./check-web-nginx.sh
  uid=$(docker compose exec -T postgres gosu postgres id -u | tr -d '\r')
  [ "$uid" != 0 ]
  docker compose exec -T postgres test -s /var/lib/postgresql/data/hengdesk-image
  ver=$(docker compose exec -T postgres psql -U hengdesk -d hengdesk -tAc \
    "SELECT extversion FROM pg_extension WHERE extname='vector'" | tr -d '[:space:]')
  [ "$ver" = 0.8.7 ]
  got=$(docker compose exec -T postgres psql -U hengdesk -d hengdesk -tAc \
    "SELECT id FROM compat_marker" | tr -d '[:space:]')
  [ "$got" = 7 ]
  printf 'uid=%s vector=%s marker=%s\n' "$uid" "$ver" "$got" | tee "$EVID/after-upgrade.txt"
)

echo "==> 在新库上备份并恢复"
(
  cd "$new_deploy"
  docker compose exec -T postgres pg_dump -U hengdesk -d hengdesk -Fc -f /backups/compat-manual.dump
  docker compose exec -T postgres psql -U hengdesk -d hengdesk -v ON_ERROR_STOP=1 \
    -c "DELETE FROM compat_marker"
  ./restore.sh compat-manual.dump -y
  got=$(docker compose exec -T postgres psql -U hengdesk -d hengdesk -tAc \
    "SELECT id FROM compat_marker" | tr -d '[:space:]')
  [ "$got" = 7 ]
  echo "restore marker=$got" | tee "$EVID/restore.txt"
)

echo "==> SIGTERM 交给正在运行的 postgres"
cid=$(docker compose -f "$new_deploy/docker-compose.yml" ps -q postgres)
docker kill --signal=TERM "$cid" >/dev/null
code=$(docker wait "$cid")
logs=$(docker logs "$cid" 2>&1 || true)
printf '%s\n' "$logs" | grep -E -q 'fast shutdown|shutting down'
[ "$code" = 0 ]
echo "signal_forward_exit=0" | tee "$EVID/signal.txt"
echo "compat_ok"
