#!/usr/bin/env bash
# 用 main 的正式离线包装出 Debian pgvector 数据，再升级到本分支的自有 Alpine 镜像。
# 各段外部等待都有上限。某一段失败会归档并继续做还能做的独立检查，最后统一返回失败。
# 信号测试会临时关掉该容器的自动重启，测完必须改回 unless-stopped。产品 compose 文件不改。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
OLD_TAR="${OLD_TAR:-/tmp/old-pack/hengdesk-offline-main-prev-amd64.tar.gz}"
NEW_TAR="${NEW_TAR:-/tmp/new-pack/hengdesk-offline-compat-new-amd64.tar.gz}"
EVID="${EVID:-/tmp/compat-evidence}"
mkdir -p "$EVID"
failed=0
signal_cid=""

diagnose() {
  local name="$1"
  {
    echo "===== $name $(date -u +%Y-%m-%dT%H:%M:%SZ) ====="
    docker ps -a --no-trunc || true
    docker compose -f "$new_deploy/docker-compose.yml" ps -a || true
    docker compose -f "$new_deploy/docker-compose.yml" logs --no-color --tail 120 || true
    docker compose -f "$old_deploy/docker-compose.yml" logs --no-color --tail 80 || true
  } >> "$EVID/diagnose.txt" 2>&1 || true
}

phase() {
  local name="$1"
  shift
  echo "==> $name"
  set +e
  (
    set -euo pipefail
    "$@"
  )
  local rc=$?
  set -e
  if [ "$rc" -eq 0 ]; then
    echo "PASS $name" | tee "$EVID/${name}.txt"
    return 0
  fi
  echo "FAIL $name rc=$rc" | tee "$EVID/${name}.txt" >&2
  diagnose "$name"
  failed=1
  return 0
}

wait_until() {
  local seconds="$1"
  local what="$2"
  shift 2
  local deadline=$((SECONDS + seconds))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if "$@"; then
      return 0
    fi
    sleep 2
  done
  echo "等待超过 ${seconds}s：$what" >&2
  return 1
}

extract() {
  local tar="$1" dest="$2"
  rm -rf "$dest"
  mkdir -p "$dest"
  tar -C "$dest" -xzf "$tar"
  find "$dest" -mindepth 1 -maxdepth 1 -type d | head -1
}

restore_restart() {
  local cid="$1"
  [ -n "$cid" ] || return 0
  echo "把 $cid 的重启策略改回 unless-stopped" | tee -a "$EVID/signal.txt"
  docker update --restart=unless-stopped "$cid" >> "$EVID/signal.txt" 2>&1 || true
  if [ -n "${new_deploy:-}" ] && [ -f "$new_deploy/docker-compose.yml" ]; then
    docker compose -f "$new_deploy/docker-compose.yml" up -d postgres >> "$EVID/signal.txt" 2>&1 || true
    local now
    now=$(docker compose -f "$new_deploy/docker-compose.yml" ps -q postgres || true)
    if [ -n "$now" ]; then
      docker inspect -f 'restart_after={{.HostConfig.RestartPolicy.Name}}' "$now" | tee -a "$EVID/signal.txt"
    fi
  fi
}

cleanup() {
  local cid=""
  if [ -f "$EVID/signal-cid" ]; then
    cid=$(cat "$EVID/signal-cid")
    restore_restart "$cid"
    rm -f "$EVID/signal-cid"
  fi
}
trap cleanup EXIT

test -f "$OLD_TAR" && test -f "$NEW_TAR"
old_root=$(extract "$OLD_TAR" /tmp/compat-old)
new_root=$(extract "$NEW_TAR" /tmp/compat-new)
old_deploy="$old_root/deploy"
new_deploy="$new_root/deploy"
grep -n 'restart:' "$new_deploy/docker-compose.yml" | tee "$EVID/compose-restart-policy.txt"

install_old() {
  (
    cd "$old_deploy"
    ./install.sh --non-interactive --ack-master-key --offline --version main-prev \
      --storage seaweedfs --admin-password ci-only-password-123 --force --up
  )
  wait_until 180 "旧包 /healthz" curl -fsS --max-time 5 localhost:8080/healthz
  timeout 30 docker compose -f "$old_deploy/docker-compose.yml" exec -T postgres \
    psql -U hengdesk -d hengdesk -v ON_ERROR_STOP=1 \
    -c "CREATE TABLE compat_marker (id int); INSERT INTO compat_marker VALUES (7);"
}

fail_drill() {
  timeout 120 docker compose -f "$old_deploy/docker-compose.yml" stop
  cp "$old_deploy/.env" "$new_deploy/.env"
  chmod 600 "$new_deploy/.env"
  set +e
  ( cd "$new_deploy" && HENGDESK_PG_MIGRATE_FAIL=after-dump timeout 180 ./pg-volume-migrate.sh )
  local rc=$?
  set -e
  [ "$rc" -eq 86 ]
  docker compose -f "$old_deploy/docker-compose.yml" up -d postgres
  wait_until 120 "旧库 pg_isready" timeout 10 docker compose -f "$old_deploy/docker-compose.yml" exec -T postgres pg_isready -U hengdesk
  local got
  got=$(timeout 20 docker compose -f "$old_deploy/docker-compose.yml" exec -T postgres \
    psql -U hengdesk -d hengdesk -tAc "SELECT id FROM compat_marker" | tr -d '[:space:]')
  [ "$got" = 7 ]
  echo "失败演练后旧库标记仍是 7"
  timeout 120 docker compose -f "$old_deploy/docker-compose.yml" stop
}

do_upgrade() {
  (
    cd "$new_deploy"
    sed -i 's/^HENGDESK_VERSION=.*/HENGDESK_VERSION=compat-new/' .env
    timeout 600 ./upgrade.sh
  )
  wait_until 180 "升级后 /healthz" curl -fsS --max-time 5 localhost:8080/healthz
  (
    cd "$new_deploy"
    timeout 30 ./check-web-nginx.sh
    uid=$(timeout 20 docker compose exec -T postgres gosu postgres id -u | tr -d '\r')
    [ "$uid" != 0 ] && [ -n "$uid" ]
    timeout 20 docker compose exec -T postgres test -s /var/lib/postgresql/data/hengdesk-image
    ver=$(timeout 20 docker compose exec -T postgres psql -U hengdesk -d hengdesk -tAc \
      "SELECT extversion FROM pg_extension WHERE extname='vector'" | tr -d '[:space:]')
    [ "$ver" = 0.8.7 ]
    got=$(timeout 20 docker compose exec -T postgres psql -U hengdesk -d hengdesk -tAc \
      "SELECT id FROM compat_marker" | tr -d '[:space:]')
    [ "$got" = 7 ]
    printf 'uid=%s vector=%s marker=%s\n' "$uid" "$ver" "$got"
  )
}

do_restore() {
  (
    cd "$new_deploy"
    timeout 180 docker compose exec -T postgres pg_dump -U hengdesk -d hengdesk -Fc -f /backups/compat-manual.dump
    timeout 20 docker compose exec -T postgres psql -U hengdesk -d hengdesk -v ON_ERROR_STOP=1 \
      -c "DELETE FROM compat_marker"
    timeout 180 ./restore.sh compat-manual.dump -y
    got=$(timeout 20 docker compose exec -T postgres psql -U hengdesk -d hengdesk -tAc \
      "SELECT id FROM compat_marker" | tr -d '[:space:]')
    [ "$got" = 7 ]
    echo "restore marker=$got"
  )
}

do_s3() {
  # 不依赖信号测试。对象存储没起来就记失败，不在这里空等。
  wait_until 60 "seaweedfs 容器在运行" \
    docker compose -f "$new_deploy/docker-compose.yml" ps --status running --quiet seaweedfs
  (
    cd "$ROOT/server"
    set -a
    # shellcheck disable=SC1091
    . "$new_deploy/.env"
    set +a
  export HENGDESK_S3_ENDPOINT=http://127.0.0.1:8333
  export HENGDESK_S3_IT=1
  timeout 180 go test -count=1 -run TestS3Integration -v ./internal/objstore/
  )
}

do_signal() {
  local cid policy code
  cid=$(docker compose -f "$new_deploy/docker-compose.yml" ps -q postgres)
  [ -n "$cid" ]
  echo "$cid" > "$EVID/signal-cid"
  policy=$(docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' "$cid")
  echo "restart_before=$policy"
  [ "$policy" = unless-stopped ]
  docker update --restart=no "$cid" >/dev/null
  echo "restart_during_test=no"
  # SIGTERM 是 smart shutdown。健康检查每 3 秒连一次，会让它一直等下去。
  # 只在这个容器上暂停健康检查，不改 compose 文件。
  docker update --health-interval=0s "$cid" >/dev/null
  echo "healthcheck_during_test=disabled"
  docker kill --signal=TERM "$cid" >/dev/null
  code=$(timeout 30 docker wait "$cid")
  echo "wait_exit=$code"
  timeout 20 docker logs "$cid" > "$EVID/signal-logs.txt" 2>&1 || true
  grep -E -q 'received (smart|fast) shutdown request' "$EVID/signal-logs.txt"
  grep -E -q 'shutting down' "$EVID/signal-logs.txt"
  [ "$code" = 0 ]
  rm -f "$EVID/signal-cid"
  # 按 compose 文件重建，恢复 unless-stopped 和原来的健康检查。
  docker compose -f "$new_deploy/docker-compose.yml" up -d --force-recreate --no-deps postgres
  local now after
  now=$(docker compose -f "$new_deploy/docker-compose.yml" ps -q postgres)
  after=$(docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' "$now")
  echo "restart_after=$after"
  [ "$after" = unless-stopped ]
  grep -q 'restart: unless-stopped' "$new_deploy/docker-compose.yml"
  echo "signal_forward_exit=0"
}

phase install-old install_old
phase fail-drill fail_drill
phase upgrade do_upgrade
phase restore do_restore
phase s3 do_s3
phase signal do_signal

if [ "$failed" -ne 0 ]; then
  echo "兼容回归有失败段，见 $EVID" | tee "$EVID/result.txt" >&2
  diagnose final
  docker compose -f "$new_deploy/docker-compose.yml" down || true
  exit 1
fi
echo "compat_ok" | tee "$EVID/result.txt"
docker compose -f "$new_deploy/docker-compose.yml" down || true
