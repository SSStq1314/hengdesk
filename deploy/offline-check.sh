#!/usr/bin/env bash
# 离线安装前自检：架构、Docker、磁盘、端口。退出码 0 表示可继续。
set -euo pipefail
die() { echo "错误：$*" >&2; exit 1; }
warn() { echo "警告：$*" >&2; }
ok() { echo "OK：$1"; }

ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ok "架构 $ARCH" ;;
  *) die "M1 仅支持 x86_64，当前为 $ARCH（ARM64 为 P1）" ;;
esac

command -v docker >/dev/null || die "未找到 docker"
docker info >/dev/null 2>&1 || die "docker 守护进程不可用（权限或未启动）"
ok "Docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo present)"
docker compose version >/dev/null 2>&1 || die "需要 Docker Compose v2（docker compose）"

# 磁盘：镜像 + 数据建议至少 20 GiB 可用
AVAIL_KB=$(df -Pk . | awk 'NR==2{print $4}')
AVAIL_GB=$((AVAIL_KB / 1024 / 1024))
if [ "$AVAIL_GB" -lt 20 ]; then
  warn "当前目录可用空间约 ${AVAIL_GB} GiB，建议 ≥ 20 GiB"
else
  ok "磁盘可用约 ${AVAIL_GB} GiB"
fi

HTTP_PORT="${HENGDESK_HTTP_PORT:-8080}"
check_port() {
  local p="$1"
  if command -v ss >/dev/null; then
    ss -ltn "sport = :$p" 2>/dev/null | grep -q ":$p" && die "端口 $p 已被占用"
  elif command -v lsof >/dev/null; then
    lsof -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1 && die "端口 $p 已被占用"
  fi
  ok "端口 $p 空闲（或未能检测，安装时再确认）"
}
check_port "$HTTP_PORT"

echo "自检通过。"
