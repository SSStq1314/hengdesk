#!/usr/bin/env bash
# D-46 第 7 条：uninstall.sh --volumes 须确认；非交互必须 --yes；secrets/ 始终保留。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/bin" "$TMP/deploy/secrets" "$TMP/backups"
echo 'fake-master-key' > "$TMP/deploy/secrets/master.key"
echo 'HENGDESK_VERSION=test' > "$TMP/deploy/.env"
cp "$ROOT/uninstall.sh" "$TMP/deploy/uninstall.sh"
chmod +x "$TMP/deploy/uninstall.sh" "$TMP/bin" 2>/dev/null || true
chmod +x "$TMP/deploy/uninstall.sh"

cat > "$TMP/bin/docker" <<'SH'
#!/usr/bin/env bash
echo "$*" >> "${HD_DOCKER_LOG:?}"
exit 0
SH
chmod +x "$TMP/bin/docker"

export PATH="$TMP/bin:$PATH"
cd "$TMP/deploy"
fail=0
assert() {
  if ! eval "$1"; then
    echo "FAIL: $2" >&2
    fail=1
  fi
}

# 1) 非交互、无 --yes，带 --volumes：非 0 退出，不调用 docker，secrets 仍在
export HD_DOCKER_LOG="$TMP/docker-1.log"
: > "$HD_DOCKER_LOG"
set +e
./uninstall.sh --volumes </dev/null
rc=$?
set -e
assert '[ "$rc" -ne 0 ]' "non-interactive --volumes without --yes should exit non-zero (got $rc)"
assert '[ ! -s "$HD_DOCKER_LOG" ]' "docker should not be invoked"
assert '[ -f secrets/master.key ]' "secrets/ must be kept"

# 2) 非交互、无 --yes，不带 --volumes：同样非 0
export HD_DOCKER_LOG="$TMP/docker-2.log"
: > "$HD_DOCKER_LOG"
set +e
./uninstall.sh </dev/null
rc=$?
set -e
assert '[ "$rc" -ne 0 ]' "non-interactive without --yes should exit non-zero (got $rc)"
assert '[ ! -s "$HD_DOCKER_LOG" ]' "docker should not be invoked without --yes"
assert '[ -f secrets/master.key ]' "secrets/ must be kept"

# 3) --volumes --yes：调用 down --volumes，提示备份，secrets 仍在
export HD_DOCKER_LOG="$TMP/docker-3.log"
: > "$HD_DOCKER_LOG"
out="$(./uninstall.sh --volumes --yes </dev/null 2>&1)"
assert '[ -s "$HD_DOCKER_LOG" ]' "docker should run with --yes --volumes"
assert 'grep -q -- "--volumes" "$HD_DOCKER_LOG"' "docker compose should receive --volumes"
assert 'printf %s "$out" | grep -q "请先做好备份"' "should prompt to back up first"
assert '[ -f secrets/master.key ]' "secrets/ must be kept after --volumes"
assert '! grep -q secrets "$HD_DOCKER_LOG"' "must not delete secrets/ via docker"

# 4) --yes 不加 --volumes：down 但不带 --volumes
export HD_DOCKER_LOG="$TMP/docker-4.log"
: > "$HD_DOCKER_LOG"
./uninstall.sh --yes </dev/null >/dev/null
assert 'grep -q "compose down" "$HD_DOCKER_LOG"' "docker compose down should run"
assert '! grep -q -- "--volumes" "$HD_DOCKER_LOG"' "without --volumes, compose must not get --volumes"
assert '[ -f secrets/master.key ]' "secrets/ must be kept"

# 交互场景需要伪终端，否则脚本会把管道 stdin 判为非交互。
run_tty() {
  local input="$1"; shift
  command -v script >/dev/null || { echo "need util-linux script for pty tests" >&2; return 127; }
  printf '%s' "$input" | script -q -e -c "$*" /dev/null
}

# 5) 交互 --volumes 输入 n：取消，不调用 docker
export HD_DOCKER_LOG="$TMP/docker-5.log"
: > "$HD_DOCKER_LOG"
set +e
out="$(run_tty $'n\n' ./uninstall.sh --volumes 2>&1)"
rc=$?
set -e
assert '[ "$rc" -ne 0 ]' "declining confirmation should exit non-zero (got $rc)"
assert '[ ! -s "$HD_DOCKER_LOG" ]' "docker should not run when cancelled"
assert '[ -f secrets/master.key ]' "secrets/ must be kept"

# 6) 交互 --volumes 输入 y：执行，secrets 仍在
export HD_DOCKER_LOG="$TMP/docker-6.log"
: > "$HD_DOCKER_LOG"
out="$(run_tty $'y\n' ./uninstall.sh --volumes 2>&1)"
assert 'grep -q -- "--volumes" "$HD_DOCKER_LOG"' "confirm y should run compose --volumes"
assert 'printf %s "$out" | grep -q "请先做好备份"' "interactive --volumes should prompt backup"
assert '[ -f secrets/master.key ]' "secrets/ must be kept after confirmed --volumes"

if [ "$fail" -ne 0 ]; then
  echo "uninstall_test.sh failed" >&2
  exit 1
fi
echo "OK：uninstall.sh D-46 确认 / --yes / secrets 保留"
