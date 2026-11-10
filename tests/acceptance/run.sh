#!/usr/bin/env bash
# M1 第八块端到端演示剧本入口。
# 用法：
#   ./tests/acceptance/run.sh --compose          # 已有 deploy/.env，叠加 acceptance compose 后跑 15 步
#   ./tests/acceptance/run.sh --base-url URL --admin-password PW [--skip-oidc]
#   ./tests/acceptance/run.sh --build-mocks      # 只构建 hengdesk/mocks:$VERSION
#   ./tests/acceptance/run.sh --compose --offline  # 离线：不 build/pull，只用已 load 的镜像
#
# 离线安装 + 15 步（CI / 验收机）见 docs/demo/m1-acceptance.md 与后续 CI job。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
COMPOSE=0
BUILD_MOCKS=0
BASE=""
ADMIN_EMAIL="${HENGDESK_ADMIN_EMAIL:-admin@hengdesk.local}"
ADMIN_PASSWORD="${M1ACCEPT_ADMIN_PASSWORD:-${HENGDESK_ADMIN_PASSWORD:-}}"
SKIP_OIDC=0
SKIP_RESTART=0
OFFLINE=0
VERSION="${HENGDESK_VERSION:-dev}"
DEPLOY="${ROOT}/deploy"

usage() { sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --compose) COMPOSE=1 ;;
    --build-mocks) BUILD_MOCKS=1 ;;
    --base-url) BASE="$2"; shift ;;
    --admin-email) ADMIN_EMAIL="$2"; shift ;;
    --admin-password) ADMIN_PASSWORD="$2"; shift ;;
    --skip-oidc) SKIP_OIDC=1 ;;
    --skip-restart) SKIP_RESTART=1 ;;
    --offline) OFFLINE=1 ;;
    --version) VERSION="$2"; shift ;;
    --deploy) DEPLOY="$2"; shift ;;
    -h|--help) usage 0 ;;
    *) echo "未知参数：$1" >&2; usage 1 ;;
  esac
  shift
done

build_mocks() {
  if [ "$OFFLINE" = 1 ]; then
    if docker image inspect "hengdesk/mocks:$VERSION" >/dev/null 2>&1; then
      echo "==> 离线模式：使用已有 hengdesk/mocks:$VERSION"
      return
    fi
    echo "离线模式但找不到镜像 hengdesk/mocks:$VERSION，请先 docker load" >&2
    exit 1
  fi
  echo "==> 构建 hengdesk/mocks:$VERSION"
  docker build -f "$ROOT/server/Dockerfile.mocks" -t "hengdesk/mocks:$VERSION" "$ROOT/server"
}

if [ "$BUILD_MOCKS" = 1 ]; then
  build_mocks
  [ "$COMPOSE" = 1 ] || [ -n "$BASE" ] || exit 0
fi

if [ "$COMPOSE" = 1 ]; then
  build_mocks
  [ -f "$DEPLOY/.env" ] || { echo "缺少 $DEPLOY/.env，请先运行 deploy/install.sh" >&2; exit 1; }
  # shellcheck disable=SC1091
  set -a; . "$DEPLOY/.env"; set +a
  ADMIN_PASSWORD="${ADMIN_PASSWORD:-${HENGDESK_ADMIN_PASSWORD:-}}"
  export HENGDESK_VERSION="$VERSION"
  echo "==> 启动演示依赖并重启应用以加载 OIDC"
  UP_FLAGS=()
  if [ "$OFFLINE" = 1 ]; then
    UP_FLAGS+=(--no-build --pull never)
    # 不要把宿主机的代理黑洞注入容器，否则 server 访问 mock 也会走 127.0.0.1:9
    unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy || true
  fi
  docker compose -f "$DEPLOY/docker-compose.yml" -f "$DEPLOY/docker-compose.acceptance.yml" up -d "${UP_FLAGS[@]}"
  # 等应用与演示依赖 healthz（mock 端口已由 acceptance compose 发布到宿主机）
  app_ok=0
  for i in $(seq 1 60); do
    if curl -fsS "${HENGDESK_HTTP_PORT:+http://127.0.0.1:${HENGDESK_HTTP_PORT}/healthz}" >/dev/null 2>&1 \
      || curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
      app_ok=1
      break
    fi
    sleep 2
  done
  if [ "$app_ok" != 1 ]; then
    echo "应用 healthz 等待超时" >&2
    exit 1
  fi
  mock_ok=0
  for i in $(seq 1 30); do
    if curl -fsS http://127.0.0.1:18081/healthz >/dev/null 2>&1 \
      && curl -fsS http://127.0.0.1:18080/healthz >/dev/null 2>&1 \
      && curl -fsS http://127.0.0.1:18082/healthz >/dev/null 2>&1; then
      mock_ok=1
      break
    fi
    sleep 1
  done
  if [ "$mock_ok" != 1 ]; then
    echo "mockoidc/mockupstream/mockredir healthz 等待超时（检查 Dockerfile.mocks 的 ENTRYPOINT 是否吞掉了 command）" >&2
    docker compose -f "$DEPLOY/docker-compose.yml" -f "$DEPLOY/docker-compose.acceptance.yml" ps -a >&2 || true
    exit 1
  fi
  ACCEPT_ENV=(
    -e M1ACCEPT_ADMIN_PASSWORD="$ADMIN_PASSWORD"
    -e M1ACCEPT_ADMIN_EMAIL="$ADMIN_EMAIL"
    -e M1ACCEPT_COMPOSE_PROJECT="${COMPOSE_PROJECT_NAME:-hengdesk}"
  )
  # AC-08 默认经容器内 docker.sock 重启 server；--skip-restart 时显式跳过
  if [ "$SKIP_RESTART" = 1 ]; then
    ACCEPT_ENV+=(-e M1ACCEPT_SKIP_RESTART=1)
  fi
  if [ "$SKIP_OIDC" = 1 ]; then
    ACCEPT_ENV+=(-e M1ACCEPT_SKIP_OIDC=1)
  fi
  if [ "$OFFLINE" = 1 ]; then
    ACCEPT_ENV+=(-e M1ACCEPT_REQUIRE_NO_PUBLIC_NET=1)
  fi
  echo "==> 运行 15 步脚本（容器内访问 http://web；AC-08 经 docker.sock 重启）"
  docker compose -f "$DEPLOY/docker-compose.yml" -f "$DEPLOY/docker-compose.acceptance.yml" \
    run --rm --no-deps \
    "${ACCEPT_ENV[@]}" \
    m1accept
  exit $?
fi

if [ -z "$BASE" ]; then
  echo "请指定 --compose 或 --base-url" >&2
  usage 1
fi
[ -n "$ADMIN_PASSWORD" ] || { echo "请提供 --admin-password" >&2; exit 1; }

echo "==> 本机编译并运行 m1accept 对 $BASE"
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.26.10}"
ARGS=( -base "$BASE" -admin-email "$ADMIN_EMAIL" -admin-password "$ADMIN_PASSWORD"
  -data-dir "$ROOT/tests/acceptance/testdata" )
[ "$SKIP_OIDC" = 1 ] && ARGS+=( -skip-oidc )
[ "$SKIP_RESTART" = 1 ] && ARGS+=( -skip-restart )
( cd "$ROOT/server" && go run ./cmd/m1accept "${ARGS[@]}" )
