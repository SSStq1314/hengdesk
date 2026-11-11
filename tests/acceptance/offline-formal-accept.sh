#!/usr/bin/env bash
# D-50：无 mock 正式包的离线验收。不调用云模型，不读取真实密钥。
# 用法（在能访问已发布端口的网络命名空间里执行）：
#   D50_ADMIN_PASSWORD=... ./offline-formal-accept.sh init --base-url http://169.254.250.1:8080
#   D50_ADMIN_PASSWORD=... ./offline-formal-accept.sh persist --base-url URL --marker d50-marker
set -euo pipefail

MODE="${1:-}"
shift || true
BASE=""
MARKER="d50-marker"
EMAIL="${D50_ADMIN_EMAIL:-admin@hengdesk.local}"
PW="${D50_ADMIN_PASSWORD:-}"

while [ $# -gt 0 ]; do
  case "$1" in
    --base-url) BASE="$2"; shift ;;
    --marker) MARKER="$2"; shift ;;
    --admin-email) EMAIL="$2"; shift ;;
    *) echo "未知参数：$1" >&2; exit 2 ;;
  esac
  shift
done

[ -n "$BASE" ] || { echo "需要 --base-url" >&2; exit 2; }
[ -n "$PW" ] || { echo "需要环境变量 D50_ADMIN_PASSWORD" >&2; exit 2; }

curl_noproxy() { curl --noproxy '*' --max-time 15 "$@"; }

wait_health() {
  local i
  for i in $(seq 1 90); do
    if curl_noproxy -fsS "$BASE/healthz" >/dev/null; then
      return 0
    fi
    sleep 2
  done
  echo "healthz 未就绪：$BASE/healthz" >&2
  return 1
}

login() {
  local cj="$1" out="$2"
  local code
  code=$(curl_noproxy -sS -o "$out" -w '%{http_code}' -c "$cj" \
    -H 'Content-Type: application/json' \
    --data-binary "{\"email\":\"${EMAIL}\",\"password\":\"${PW}\"}" \
    "$BASE/api/v1/auth/login")
  [ "$code" = 200 ] || { echo "登录失败 HTTP $code" >&2; return 1; }
}

csrf_of() { awk '$6=="hd_csrf"{print $7}' "$1" | tail -1; }

case "$MODE" in
  init)
    wait_health
    echo "healthz 通过"
    code=$(curl_noproxy -sS -o /dev/null -w '%{http_code}' "$BASE/api/v1/auth/me")
    [ "$code" = 401 ] || { echo "未登录 /auth/me 期望 401，实际 $code" >&2; exit 1; }
    echo "未登录 401 通过"
    code=$(curl_noproxy -sS -o /dev/null -w '%{http_code}' "$BASE/api/v1/admin/departments")
    [ "$code" = 401 ] || { echo "未登录部门列表期望 401，实际 $code" >&2; exit 1; }
    cj=$(mktemp)
    body=$(mktemp)
    trap 'rm -f "$cj" "$body"' EXIT
    login "$cj" "$body"
    echo "管理员登录通过"
    csrf=$(csrf_of "$cj")
    [ -n "$csrf" ] || { echo "缺少 CSRF cookie" >&2; exit 1; }
    code=$(curl_noproxy -sS -o "$body" -w '%{http_code}' -b "$cj" \
      -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' \
      --data-binary "{\"name\":\"${MARKER}\"}" \
      "$BASE/api/v1/admin/departments")
    [ "$code" = 201 ] || { echo "创建部门失败 HTTP $code" >&2; cat "$body" >&2; exit 1; }
    echo "已创建持久化标记部门 $MARKER"
    ;;
  persist)
    wait_health
    echo "重启后 healthz 通过"
    code=$(curl_noproxy -sS -o /dev/null -w '%{http_code}' "$BASE/api/v1/auth/me")
    [ "$code" = 401 ] || { echo "重启后未登录期望 401，实际 $code" >&2; exit 1; }
    cj=$(mktemp)
    body=$(mktemp)
    trap 'rm -f "$cj" "$body"' EXIT
    login "$cj" "$body"
    code=$(curl_noproxy -sS -o "$body" -w '%{http_code}' -b "$cj" "$BASE/api/v1/admin/departments")
    [ "$code" = 200 ] || { echo "重启后部门列表 HTTP $code" >&2; exit 1; }
    grep -q "\"name\":\"${MARKER}\"" "$body" || grep -q "\"name\": \"${MARKER}\"" "$body" || {
      echo "重启后找不到标记部门 $MARKER" >&2
      exit 1
    }
    echo "重启后标记部门仍在，数据持久性通过"
    ;;
  *)
    echo "用法：$0 init|persist --base-url URL" >&2
    exit 2
    ;;
esac
