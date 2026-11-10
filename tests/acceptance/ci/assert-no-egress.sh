#!/usr/bin/env bash
# CI / 验收机上的断网模拟层（iptables），不是产品出站控制。
# 不修改 server/internal/egress（D-47：DNS 默认 local_verify）。
#
# 只放行 RFC1918 / 本机回环（含 Docker 内嵌 DNS 127.0.0.11）以及 compose 网段
# （172.16.0.0/12，覆盖 docker0 与 compose 的 172.17/172.18…）。
# 不放行到 8.8.8.8、1.1.1.1 等外网解析器。
#
# 用法：
#   sudo ./assert-no-egress.sh start
#   sudo ./assert-no-egress.sh check   # 内核日志出现 HD_EGRESS 则失败
#   sudo ./assert-no-egress.sh stop
set -euo pipefail
CMD="${1:-}"
CHAIN4=HD_NO_EGRESS
CHAIN6=HD_NO_EGRESS6
PREFIX="HD_EGRESS "
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo >/dev/null && sudo -n true 2>/dev/null; then
    SUDO=sudo
  else
    echo "需要 root 或可用的 sudo 才能装出站拦截规则" >&2
    exit 2
  fi
fi

ensure_docker_user() {
  $SUDO iptables -w -nL DOCKER-USER >/dev/null 2>&1 || $SUDO iptables -w -N DOCKER-USER
}

install4() {
  ensure_docker_user
  $SUDO iptables -w -N "$CHAIN4" 2>/dev/null || $SUDO iptables -w -F "$CHAIN4"
  $SUDO iptables -w -A "$CHAIN4" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  # 私网与回环（含 127.0.0.11）。不要写成 172.16.0.0/16，否则会误伤 172.17/172.18 的 compose 网。
  for cidr in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8; do
    $SUDO iptables -w -A "$CHAIN4" -d "$cidr" -j RETURN
  done
  $SUDO iptables -w -A "$CHAIN4" -j LOG --log-prefix "$PREFIX" --log-level 4
  $SUDO iptables -w -A "$CHAIN4" -j REJECT
  $SUDO iptables -w -C DOCKER-USER -j "$CHAIN4" 2>/dev/null || $SUDO iptables -w -I DOCKER-USER -j "$CHAIN4"
}

install6() {
  if ! command -v ip6tables >/dev/null; then
    return 0
  fi
  $SUDO ip6tables -w -nL DOCKER-USER >/dev/null 2>&1 || $SUDO ip6tables -w -N DOCKER-USER 2>/dev/null || true
  if ! $SUDO ip6tables -w -nL DOCKER-USER >/dev/null 2>&1; then
    return 0
  fi
  $SUDO ip6tables -w -N "$CHAIN6" 2>/dev/null || $SUDO ip6tables -w -F "$CHAIN6"
  $SUDO ip6tables -w -A "$CHAIN6" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  for cidr in fc00::/7 fe80::/10 ::1/128; do
    $SUDO ip6tables -w -A "$CHAIN6" -d "$cidr" -j RETURN
  done
  $SUDO ip6tables -w -A "$CHAIN6" -j LOG --log-prefix "$PREFIX" --log-level 4
  $SUDO ip6tables -w -A "$CHAIN6" -j REJECT
  $SUDO ip6tables -w -C DOCKER-USER -j "$CHAIN6" 2>/dev/null || $SUDO ip6tables -w -I DOCKER-USER -j "$CHAIN6"
}

remove4() {
  if $SUDO iptables -w -nL DOCKER-USER >/dev/null 2>&1; then
    $SUDO iptables -w -D DOCKER-USER -j "$CHAIN4" 2>/dev/null || true
  fi
  $SUDO iptables -w -F "$CHAIN4" 2>/dev/null || true
  $SUDO iptables -w -X "$CHAIN4" 2>/dev/null || true
}

remove6() {
  command -v ip6tables >/dev/null || return 0
  $SUDO ip6tables -w -D DOCKER-USER -j "$CHAIN6" 2>/dev/null || true
  $SUDO ip6tables -w -F "$CHAIN6" 2>/dev/null || true
  $SUDO ip6tables -w -X "$CHAIN6" 2>/dev/null || true
}

hits() {
  dmesg -T 2>/dev/null | grep -F "$PREFIX" || true
  journalctl -k -n 500 --no-pager 2>/dev/null | grep -F "$PREFIX" || true
}

case "$CMD" in
  start)
    install4
    install6
    echo "已启用 Docker 公网出站拦截（仅放行私网/回环，日志前缀 $PREFIX）"
    ;;
  check)
    found="$(hits | sort -u)"
    if [ -n "$found" ]; then
      echo "检测到公网出站尝试：" >&2
      echo "$found" >&2
      exit 1
    fi
    echo "未在内核日志中发现 ${PREFIX} 出站记录"
    ;;
  stop)
    remove4
    remove6
    echo "已拆除出站拦截规则"
    ;;
  *)
    echo "用法：$0 start|check|stop" >&2
    exit 2
    ;;
esac
