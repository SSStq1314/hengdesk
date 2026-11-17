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

activity_dump() {
  local dest="$1"
  timeout 20 docker compose -f "$new_deploy/docker-compose.yml" exec -T postgres \
    psql -U hengdesk -d hengdesk -v ON_ERROR_STOP=1 -c \
    "SELECT pid, usename, application_name, client_addr::text, state, backend_type, wait_event_type, left(query, 160) AS query
     FROM pg_stat_activity
     WHERE pid <> pg_backend_pid()
     ORDER BY backend_type, application_name, pid;" > "$dest"
}

stop_timeout_of() {
  local raw="$1"
  case "$raw" in
    ''|*[!0-9]*) echo "StopTimeout 不是整数：$raw" >&2; return 1 ;;
  esac
  echo "$raw"
}

# 隔离容器：没有健康检查、没有应用连接。用镜像自己的 StopSignal 停，等价于 docker stop。
# 这只证明入口和 gosu 把停机信号交给了 postgres。它不是正式 compose 的 stop 路径。
do_isolated_stop() {
  local name="hd-sig-iso-$$"
  local img="hengdesk/postgres:compat-new"
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" --restart=no \
    -e POSTGRES_PASSWORD=ci-only-password-123 -e POSTGRES_USER=hengdesk -e POSTGRES_DB=hengdesk \
    "$img" >/dev/null
  wait_until 120 "隔离库就绪" timeout 10 docker exec "$name" pg_isready -U hengdesk
  local stop_signal grace
  stop_signal=$(docker inspect -f '{{.Config.StopSignal}}' "$name")
  grace=$(stop_timeout_of "$(docker inspect -f '{{.Config.StopTimeout}}' "$name")")
  echo "isolated_stopsignal=$stop_signal stop_timeout=${grace}s"
  [ -n "$stop_signal" ]
  timeout $((grace + 20)) docker stop -t "$grace" "$name" >/dev/null
  local code
  code=$(docker inspect -f '{{.State.ExitCode}}' "$name")
  echo "isolated_exit=$code"
  docker logs "$name" > "$EVID/isolated-signal-logs.txt" 2>&1 || true
  grep -E -q 'received (smart|fast) shutdown request' "$EVID/isolated-signal-logs.txt"
  grep -E -q 'database system is shut down' "$EVID/isolated-signal-logs.txt"
  [ "$code" = 0 ]
  docker rm -f "$name" >/dev/null
}

# 单独容器上观察 SIGTERM（smart shutdown），不改正式 compose 的 healthcheck 或 restart。
# 先占住一个会话，再发 SIGTERM，然后立刻尝试新连接。新连接应被拒绝；占住的会话结束后进程以 0 退出。
do_smart_probe() {
  local name="hd-smart-$$"
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" --restart=no \
    -e POSTGRES_PASSWORD=ci-only-password-123 -e POSTGRES_USER=hengdesk -e POSTGRES_DB=hengdesk \
    hengdesk/postgres:compat-new >/dev/null
  wait_until 120 "smart 探针库就绪" timeout 10 docker exec "$name" pg_isready -U hengdesk
  timeout 5 docker exec -d "$name" psql -U hengdesk -d hengdesk -c "SELECT pg_sleep(15)"
  sleep 1
  timeout 15 docker exec "$name" psql -U hengdesk -d hengdesk -tAc \
    "SELECT pid, state, left(query, 80) FROM pg_stat_activity WHERE query LIKE '%pg_sleep%'" \
    | tee "$EVID/smart-holder.txt"
  grep -q pg_sleep "$EVID/smart-holder.txt"
  docker kill --signal=TERM "$name" >/dev/null
  set +e
  local newc nrc
  newc=$(timeout 10 docker exec "$name" psql -U hengdesk -d hengdesk -tAc 'SELECT 1' 2>&1)
  nrc=$?
  set -e
  {
    echo "new_connection_rc=$nrc"
    echo "$newc"
  } | tee "$EVID/smart-new-connection.txt"
  echo "$newc" | grep -E -q 'shutting down|the database system is shutting down|terminating connection'
  local code
  code=$(timeout 40 docker wait "$name")
  echo "smart_probe_exit=$code"
  docker logs "$name" > "$EVID/smart-probe-logs.txt" 2>&1 || true
  grep -q 'received smart shutdown request' "$EVID/smart-probe-logs.txt"
  grep -q 'database system is shut down' "$EVID/smart-probe-logs.txt"
  [ "$code" = 0 ]
  docker rm -f "$name" >/dev/null
}

# 正式 compose：不改 healthcheck，不改 restart。停机信号和宽限以容器实际配置为准。
do_compose_stop() {
  local cid stop_signal grace code
  cid=$(docker compose -f "$new_deploy/docker-compose.yml" ps -q postgres)
  [ -n "$cid" ]
  stop_signal=$(docker inspect -f '{{.Config.StopSignal}}' "$cid")
  grace=$(stop_timeout_of "$(docker inspect -f '{{.Config.StopTimeout}}' "$cid")")
  local restart health
  restart=$(docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' "$cid")
  health=$(docker inspect -f '{{.Config.Healthcheck.Interval}}' "$cid")
  {
    echo "compose_stopsignal=$stop_signal"
    echo "compose_stop_timeout_seconds=$grace"
    echo "restart_before=$restart"
    echo "healthcheck_interval_ns=$health"
    echo "compose_file_restart=$(grep -n 'restart:' "$new_deploy/docker-compose.yml" | head -5)"
  } | tee "$EVID/stop-config.txt"
  [ "$restart" = unless-stopped ]
  [ "$grace" -ge 1 ]
  activity_dump "$EVID/pg-stat-activity-before-stop.txt"
  echo "----- 停机前会话（本查询自身已排除） -----"
  cat "$EVID/pg-stat-activity-before-stop.txt"
  # 不使用 docker kill --signal=TERM。那会绕过镜像 StopSignal。
  # SIGTERM 在 Postgres 上是 smart shutdown；compose stop 发送的是上面记录的 StopSignal。
  if ! timeout $((grace + 15)) docker compose -f "$new_deploy/docker-compose.yml" stop -t "$grace" postgres; then
    echo "compose stop 超过 $((grace + 15))s" >&2
    return 1
  fi
  code=$(docker inspect -f '{{.State.ExitCode}}' "$cid")
  echo "compose_stop_exit=$code"
  docker logs --tail 80 "$cid" > "$EVID/compose-stop-logs.txt" 2>&1 || true
  grep -E -q 'received (smart|fast|immediate) shutdown request' "$EVID/compose-stop-logs.txt"
  grep -E -q 'database system is shut down' "$EVID/compose-stop-logs.txt"
  if [ "$code" = 137 ]; then
    echo "postgres 以 SIGKILL 结束，不能算优雅停止。停机前会话见 pg-stat-activity-before-stop.txt" >&2
    return 1
  fi
  [ "$code" = 0 ]
  docker compose -f "$new_deploy/docker-compose.yml" up -d --no-deps postgres
  wait_until 120 "停机后 postgres 恢复" timeout 10 docker compose -f "$new_deploy/docker-compose.yml" exec -T postgres pg_isready -U hengdesk
  local now after health2 uid ver got
  now=$(docker compose -f "$new_deploy/docker-compose.yml" ps -q postgres)
  after=$(docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' "$now")
  health2=$(docker inspect -f '{{.Config.Healthcheck.Interval}}' "$now")
  echo "restart_after=$after healthcheck_interval_ns_after=$health2"
  [ "$after" = unless-stopped ]
  [ "$health2" = "$health" ]
  uid=$(timeout 20 docker compose -f "$new_deploy/docker-compose.yml" exec -T postgres gosu postgres id -u | tr -d '\r')
  ver=$(timeout 20 docker compose -f "$new_deploy/docker-compose.yml" exec -T postgres psql -U hengdesk -d hengdesk -tAc \
    "SELECT extversion FROM pg_extension WHERE extname='vector'" | tr -d '[:space:]')
  got=$(timeout 20 docker compose -f "$new_deploy/docker-compose.yml" exec -T postgres psql -U hengdesk -d hengdesk -tAc \
    "SELECT id FROM compat_marker" | tr -d '[:space:]')
  echo "uid=$uid vector=$ver marker=$got"
  [ "$uid" != 0 ] && [ -n "$uid" ]
  [ "$ver" = 0.8.7 ]
  [ "$got" = 7 ]
}

phase install-old install_old
phase fail-drill fail_drill
phase upgrade do_upgrade
phase restore do_restore
phase s3 do_s3
phase isolated-stop do_isolated_stop
phase smart-probe do_smart_probe
phase compose-stop do_compose_stop

if [ "$failed" -ne 0 ]; then
  echo "兼容回归有失败段，见 $EVID" | tee "$EVID/result.txt" >&2
  diagnose final
  docker compose -f "$new_deploy/docker-compose.yml" down || true
  exit 1
fi
echo "compat_ok" | tee "$EVID/result.txt"
docker compose -f "$new_deploy/docker-compose.yml" down || true
