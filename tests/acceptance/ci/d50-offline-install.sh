#!/usr/bin/env bash
# D-50：默认正式包（不含 mocks）在隔离之后加载，并在新库上做健康检查、离线验收、重启持久性。
# 准备阶段只允许宿主依赖（Docker / Compose）和已经打好的正式包。
# 产品、数据库、缓存、对象存储镜像必须在隔离后由包内 tar load。
#
# 控制通道：init 网络命名空间中的 runner uid 仍可访问 GitHub（心跳与结束后的 artifact 上传）。
# 安装进程在 netns hd-sut，容器转发与 dockerd 的新公网连接被拒绝。安装进程和产品容器不能借用该通道。
# 这只证明本次 GitHub 托管 x86 VM，不证明物理机或客户现场。
#
# 用法（root）：
#   d50-offline-install.sh run --tar PKG.tar.gz --evidence DIR
#   d50-offline-install.sh teardown
set -euo pipefail

STATE=/var/run/hengdesk-d50.state
CMD="${1:-}"
shift || true

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

need_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "需要 root" >&2
    exit 2
  fi
}

reject_pkts() {
  local bin="$1" chain="$2" table="${3:-filter}"
  "$bin" -t "$table" -L "$chain" -v -n -x 2>/dev/null | awk '$3=="REJECT" || $3=="DROP"{s+=$1} END{print s+0}'
}

snapshot_counters() {
  local dest="$1"
  {
    echo "# $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    for c in HD_VETH4 HD_DOCK4 HD_ROOT HD_DNS HD_OUT4; do
      echo "iptables $c $(reject_pkts iptables "$c")"
    done
    for c in HD_VETH6 HD_DOCK6 HD_OUT6; do
      echo "ip6tables $c $(reject_pkts ip6tables "$c")"
    done
    echo "ip6tables-raw HD_V6RAW $(reject_pkts ip6tables HD_V6RAW raw)"
  } > "$dest"
}

counter_of() {
  local file="$1" key="$2"
  awk -v k="$key" '$1" "$2==k {print $3}' "$file"
}

# 安装和验收窗口里的拒绝计数是被拦住的出站尝试，写入证据，不把「规则生效」当成失败。
# 真正的失败是探针连上，或安装过程拉取了镜像。
record_blocks() {
  local before="$1" after="$2" label="$3"
  local key b a
  {
    echo "# $label"
    while read -r key; do
      [ -n "$key" ] || continue
      b=$(counter_of "$before" "$key")
      a=$(counter_of "$after" "$key")
      echo "$key ${b:-0} -> ${a:-0}"
    done <<'KEYS'
iptables HD_VETH4
iptables HD_DOCK4
iptables HD_ROOT
iptables HD_DNS
iptables HD_OUT4
ip6tables HD_VETH6
ip6tables HD_DOCK6
ip6tables HD_OUT6
ip6tables-raw HD_V6RAW
KEYS
  } | tee -a "$EVID/blocked-during-product.txt"
}

assert_increase() {
  local before="$1" after="$2" key="$3"
  local b a
  b=$(counter_of "$before" "$key")
  a=$(counter_of "$after" "$key")
  if [ "${a:-0}" -le "${b:-0}" ]; then
    echo "探针未打到 $key（${b:-0} -> ${a:-0}）" | tee -a "$EVID/violations.txt"
    return 1
  fi
  echo "探针命中 $key ${b:-0} -> ${a:-0}"
}

record_docker() {
  local name="$1"
  {
    echo "# $name $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "## images"
    docker images --digests --no-trunc || true
    echo "## ps"
    docker ps -a --no-trunc || true
    echo "## volumes"
    docker volume ls || true
    echo "## networks"
    docker network ls || true
  } > "$EVID/docker-$name.txt"
}

assert_clean_baseline() {
  if grep -E 'hengdesk|pgvector|valkey|seaweedfs|minio' "$EVID/docker-baseline.txt"; then
    echo "清洁基线仍有产品或依赖镜像/卷/网络，不能开始隔离安装" | tee -a "$EVID/violations.txt"
    return 1
  fi
  echo "清洁基线未发现产品镜像、依赖镜像、相关卷或网络名"
}

clean_runtime() {
  log "清除容器、卷、镜像和构建缓存（正式包 tar 保留在磁盘上）"
  docker ps -aq | xargs -r docker rm -f || true
  local root="${GITHUB_WORKSPACE:-/workspace}"
  if [ -f "$root/deploy/docker-compose.yml" ]; then
    docker compose -f "$root/deploy/docker-compose.yml" down --volumes --remove-orphans || true
  fi
  docker volume ls -q | xargs -r docker volume rm -f || true
  docker network prune -f || true
  docker images -aq | xargs -r docker rmi -f || true
  docker builder prune -af || true
  rm -rf /root/.cache/huggingface /home/runner/.cache/huggingface /tmp/huggingface \
    "${SUDO_USER:+/home/$SUDO_USER/.cache/huggingface}" || true
}

enable_docker_ipv6() {
  mkdir -p /etc/docker
  if [ -f /etc/docker/daemon.json ]; then
    cp -a /etc/docker/daemon.json /etc/docker/daemon.json.d50-bak
    echo "DAEMON_BAK=1" >> "$STATE"
  else
    echo "DAEMON_BAK=0" >> "$STATE"
  fi
  python3 - << 'PY'
import json, os
p = "/etc/docker/daemon.json"
data = {}
if os.path.exists(p) and os.path.getsize(p):
    data = json.load(open(p))
data["ipv6"] = True
data["fixed-cidr-v6"] = "fd00:50:1::/64"
data["ip6tables"] = True
json.dump(data, open(p, "w"), indent=2)
open(p, "a").write("\n")
PY
  log "重启 docker 以启用 IPv6 / ip6tables"
  systemctl restart docker
  local i
  for i in $(seq 1 30); do
    if docker info >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "docker 重启后不可用" | tee -a "$EVID/violations.txt"
  return 1
}

setup_netns() {
  ip netns del hd-sut 2>/dev/null || true
  ip link del veth-hd-host 2>/dev/null || true
  ip netns add hd-sut
  ip link add veth-hd-host type veth peer name veth-hd-ns
  ip link set veth-hd-ns netns hd-sut
  ip addr add 169.254.250.1/30 dev veth-hd-host
  sysctl -w net.ipv6.conf.all.forwarding=1 >/dev/null
  sysctl -w net.ipv6.conf.default.forwarding=1 >/dev/null
  ip link set veth-hd-host up
  sysctl -w net.ipv6.conf.veth-hd-host.forwarding=1 >/dev/null
  sysctl -w net.ipv6.conf.veth-hd-host.accept_dad=0 >/dev/null
  ip -6 addr add fd00:50::1/64 dev veth-hd-host
  ip netns exec hd-sut ip addr add 169.254.250.2/30 dev veth-hd-ns
  ip netns exec hd-sut ip link set veth-hd-ns up
  ip netns exec hd-sut ip link set lo up
  ip netns exec hd-sut sysctl -w net.ipv6.conf.veth-hd-ns.accept_dad=0 >/dev/null
  ip netns exec hd-sut ip -6 addr add fd00:50::2/64 dev veth-hd-ns
  # 默认路由让探针报文真正进入宿主转发链，再由下面的 REJECT 丢掉。
  # all.forwarding 不会回头打开已经存在的网卡，所以必须单独打开 veth。
  # 永久邻居避免 IPv6 网关因邻居发现失败变成 Network is unreachable，报文根本不进 FORWARD。
  ip netns exec hd-sut ip route add default via 169.254.250.1
  host_mac=$(cat /sys/class/net/veth-hd-host/address)
  ip netns exec hd-sut ip -6 neigh add fd00:50::1 lladdr "$host_mac" dev veth-hd-ns nud permanent
  ip netns exec hd-sut ip -6 route add default via fd00:50::1
  # 从 veth 进来的非邻居发现 IPv6 在 PREROUTING 丢掉并计数。
  # 只靠 filter FORWARD 时，报文可能在更早的钩子被丢掉，计数保持 0。
  ip6tables -t raw -N HD_V6RAW 2>/dev/null || ip6tables -t raw -F HD_V6RAW
  ip6tables -t raw -A HD_V6RAW -j LOG --log-prefix "HD_V6RAW " --log-level 4
  ip6tables -t raw -A HD_V6RAW -j DROP
  ip6tables -t raw -C PREROUTING -i veth-hd-host ! -p ipv6-icmp -j HD_V6RAW 2>/dev/null || \
    ip6tables -t raw -I PREROUTING 1 -i veth-hd-host ! -p ipv6-icmp -j HD_V6RAW
  sysctl -w net.ipv4.ip_forward=1 >/dev/null
  # 只允许该 veth 访问宿主上已发布的 8080，其余输入拒绝，避免把宿主当代理。
  iptables -I INPUT 1 -i veth-hd-host -j REJECT
  iptables -I INPUT 1 -i veth-hd-host -p tcp --dport 8080 -j ACCEPT
  # IPv6 邻居发现是发给宿主的 ICMPv6。先整段 REJECT 会让网关不可达，
  # TCP 探测超时且不会进入 FORWARD，HD_VETH6 计数保持 0。
  ip6tables -I INPUT 1 -i veth-hd-host -j REJECT || true
  ip6tables -I INPUT 1 -i veth-hd-host -p icmpv6 -j ACCEPT || true
  echo "NETNS=1" >> "$STATE"
}

add_chain() {
  local bin="$1" chain="$2" prefix="$3"
  "$bin" -N "$chain" 2>/dev/null || "$bin" -F "$chain"
  "$bin" -A "$chain" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  "$bin" -A "$chain" -j LOG --log-prefix "$prefix" --log-level 4
  "$bin" -A "$chain" -j REJECT
}

setup_filters() {
  local runner_uid="$1" dockerd_uid="$2"
  add_chain iptables HD_VETH4 "HD_VETH4 "
  add_chain ip6tables HD_VETH6 "HD_VETH6 "
  iptables -C FORWARD -i veth-hd-host -j HD_VETH4 2>/dev/null || iptables -I FORWARD 1 -i veth-hd-host -j HD_VETH4
  ip6tables -C FORWARD -i veth-hd-host -j HD_VETH6 2>/dev/null || ip6tables -I FORWARD 1 -i veth-hd-host -j HD_VETH6

  add_chain iptables HD_DOCK4 "HD_DOCK4 "
  # 容器访问私网 / 回环放行（含 Docker DNS 127.0.0.11 与 compose 网段）。公网拒绝。
  iptables -I HD_DOCK4 1 -d 192.168.0.0/16 -j RETURN
  iptables -I HD_DOCK4 1 -d 172.16.0.0/12 -j RETURN
  iptables -I HD_DOCK4 1 -d 10.0.0.0/8 -j RETURN
  iptables -I HD_DOCK4 1 -d 127.0.0.0/8 -j RETURN
  iptables -I HD_DOCK4 1 -d 169.254.0.0/16 -j RETURN
  iptables -N DOCKER-USER 2>/dev/null || true
  iptables -C DOCKER-USER -j HD_DOCK4 2>/dev/null || iptables -I DOCKER-USER 1 -j HD_DOCK4

  add_chain ip6tables HD_DOCK6 "HD_DOCK6 "
  ip6tables -I HD_DOCK6 1 -d fe80::/10 -j RETURN
  ip6tables -I HD_DOCK6 1 -d fc00::/7 -j RETURN
  ip6tables -I HD_DOCK6 1 -d ::1/128 -j RETURN
  ip6tables -N DOCKER-USER 2>/dev/null || true
  ip6tables -C DOCKER-USER -j HD_DOCK6 2>/dev/null || ip6tables -I DOCKER-USER 1 -j HD_DOCK6

  # dockerd/containerd 所在 uid 的新公网连接拒绝。已建立连接和私网放行。
  iptables -N HD_ROOT 2>/dev/null || iptables -F HD_ROOT
  iptables -A HD_ROOT -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  iptables -A HD_ROOT -o lo -j RETURN
  iptables -A HD_ROOT -d 10.0.0.0/8 -j RETURN
  iptables -A HD_ROOT -d 172.16.0.0/12 -j RETURN
  iptables -A HD_ROOT -d 192.168.0.0/16 -j RETURN
  iptables -A HD_ROOT -d 127.0.0.0/8 -j RETURN
  iptables -A HD_ROOT -d 169.254.0.0/16 -j RETURN
  iptables -A HD_ROOT -j LOG --log-prefix "HD_ROOT " --log-level 4
  iptables -A HD_ROOT -j REJECT
  iptables -C OUTPUT -m owner --uid-owner "$dockerd_uid" -j HD_ROOT 2>/dev/null || \
    iptables -I OUTPUT 1 -m owner --uid-owner "$dockerd_uid" -j HD_ROOT

  # 公网 DNS 只留给 runner uid。堵住 dockerd / resolved 代解析。
  iptables -N HD_DNS 2>/dev/null || iptables -F HD_DNS
  iptables -A HD_DNS -m owner --uid-owner "$runner_uid" -j RETURN
  iptables -A HD_DNS -d 10.0.0.0/8 -j RETURN
  iptables -A HD_DNS -d 172.16.0.0/12 -j RETURN
  iptables -A HD_DNS -d 192.168.0.0/16 -j RETURN
  iptables -A HD_DNS -d 127.0.0.0/8 -j RETURN
  iptables -A HD_DNS -j LOG --log-prefix "HD_DNS " --log-level 4
  iptables -A HD_DNS -j REJECT
  iptables -C OUTPUT -p udp --dport 53 -j HD_DNS 2>/dev/null || iptables -I OUTPUT 1 -p udp --dport 53 -j HD_DNS
  iptables -C OUTPUT -p tcp --dport 53 -j HD_DNS 2>/dev/null || iptables -I OUTPUT 1 -p tcp --dport 53 -j HD_DNS

  # 新的公网 OUTPUT 只放行 runner 控制通道所在 cgroup。
  # 安装进程被挪到 hd-sut cgroup：就算 nsenter 回 init 网络命名空间，或 setuid 成 runner，也不能新建公网连接。
  # 已建立连接按五元组匹配，别的进程没有 runner 的 socket，不能借那条 TCP。
  local cg="${RUNNER_CGROUP:-}"
  if [ -z "$cg" ]; then
    echo "未设置 RUNNER_CGROUP" >&2
    return 1
  fi
  iptables -N HD_OUT4 2>/dev/null || iptables -F HD_OUT4
  iptables -A HD_OUT4 -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  iptables -A HD_OUT4 -o lo -j RETURN
  iptables -A HD_OUT4 -d 10.0.0.0/8 -j RETURN
  iptables -A HD_OUT4 -d 172.16.0.0/12 -j RETURN
  iptables -A HD_OUT4 -d 192.168.0.0/16 -j RETURN
  iptables -A HD_OUT4 -d 127.0.0.0/8 -j RETURN
  iptables -A HD_OUT4 -d 169.254.0.0/16 -j RETURN
  if ! iptables -A HD_OUT4 -m cgroup --path "$cg" -j RETURN; then
    echo "iptables 不支持 cgroup --path，不能证明提权后的安装进程用不上控制通道" >&2
    return 1
  fi
  iptables -A HD_OUT4 -j LOG --log-prefix "HD_OUT4 " --log-level 4
  iptables -A HD_OUT4 -j REJECT
  iptables -C OUTPUT -j HD_OUT4 2>/dev/null || iptables -I OUTPUT 1 -j HD_OUT4

  ip6tables -N HD_OUT6 2>/dev/null || ip6tables -F HD_OUT6
  ip6tables -A HD_OUT6 -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  ip6tables -A HD_OUT6 -o lo -j RETURN
  ip6tables -A HD_OUT6 -d fc00::/7 -j RETURN
  ip6tables -A HD_OUT6 -d fe80::/10 -j RETURN
  ip6tables -A HD_OUT6 -d ::1/128 -j RETURN
  ip6tables -A HD_OUT6 -m cgroup --path "$cg" -j RETURN
  ip6tables -A HD_OUT6 -j LOG --log-prefix "HD_OUT6 " --log-level 4
  ip6tables -A HD_OUT6 -j REJECT
  ip6tables -C OUTPUT -j HD_OUT6 2>/dev/null || ip6tables -I OUTPUT 1 -j HD_OUT6

  echo "RUNNER_UID=$runner_uid" >> "$STATE"
  echo "DOCKERD_UID=$dockerd_uid" >> "$STATE"
  echo "FILTERS=1" >> "$STATE"
}

reassert_docker_user() {
  iptables -C DOCKER-USER -j HD_DOCK4 2>/dev/null || iptables -I DOCKER-USER 1 -j HD_DOCK4
  ip6tables -C DOCKER-USER -j HD_DOCK6 2>/dev/null || ip6tables -I DOCKER-USER 1 -j HD_DOCK6
}

probe_netns() {
  local gh_ip="$1"
  ip netns exec hd-sut python3 - "$gh_ip" << 'PY' > "$EVID/probe-netns.txt"
import socket, sys
gh = sys.argv[1]
fail = 0
def tcp(family, addr, port):
    global fail
    s = socket.socket(family, socket.SOCK_STREAM)
    s.settimeout(3)
    try:
        s.connect((addr, port))
    except Exception as e:
        print(f"FAIL tcp {addr}:{port} {type(e).__name__}: {e}")
        return
    print(f"UNEXPECTED_SUCCESS tcp {addr}:{port}")
    fail = 1
tcp(socket.AF_INET, "1.1.1.1", 443)
tcp(socket.AF_INET, "8.8.8.8", 53)
tcp(socket.AF_INET6, "2606:4700:4700::1111", 443)
if gh:
    tcp(socket.AF_INET, gh, 443)
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(3)
try:
    s.sendto(b"\x00\x01\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x03com\x00\x00\x01\x00\x01", ("8.8.8.8", 53))
    s.recvfrom(512)
    print("UNEXPECTED_SUCCESS udp/53 8.8.8.8")
    fail = 1
except Exception as e:
    print(f"FAIL udp/53 8.8.8.8 {type(e).__name__}: {e}")
try:
    socket.getaddrinfo("example.com", 443)
    print("UNEXPECTED_SUCCESS dns example.com")
    fail = 1
except Exception as e:
    print(f"FAIL dns example.com {type(e).__name__}: {e}")
sys.exit(fail)
PY
}

probe_container() {
  local image="$1" net="$2" gh_ip="$3" out="$4" name="$5"
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" --network "$net" --entrypoint sleep "$image" 180 >/dev/null
  local pid gw
  pid=$(docker inspect -f '{{.State.Pid}}' "$name")
  local nid br mac gw
  nid=$(docker network inspect "$net" -f '{{.Id}}')
  br="br-${nid:0:12}"
  mac=$(cat "/sys/class/net/$br/address" 2>/dev/null || true)
  gw=$(nsenter -t "$pid" -n ip -6 route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="via"){print $(i+1); exit}}')
  {
    echo "bridge=$br mac=${mac:-missing} gw=${gw:-missing}"
    nsenter -t "$pid" -n ip -6 neigh show || true
  } >> "$out.route6"
  if [ -n "$mac" ]; then
    nsenter -t "$pid" -n ip -6 neigh replace 2606:4700:4700::1111 lladdr "$mac" dev eth0 nud permanent || true
    nsenter -t "$pid" -n ip -6 route replace 2606:4700:4700::1111/128 dev eth0 || true
    if [ -n "$gw" ]; then
      nsenter -t "$pid" -n ip -6 neigh replace "$gw" lladdr "$mac" dev eth0 nud permanent || true
    fi
  fi
  nsenter -t "$pid" -n ip route > "$out.route4" 2>&1 || true
  nsenter -t "$pid" -n ip -6 route >> "$out.route6" 2>&1 || true
  if ! nsenter -t "$pid" -n python3 - "$gh_ip" > "$out" << 'PY'
import socket, sys
gh = sys.argv[1]
fail = 0
def tcp(family, addr, port):
    global fail
    s = socket.socket(family, socket.SOCK_STREAM)
    s.settimeout(3)
    try:
        s.connect((addr, port))
    except Exception as e:
        print(f"FAIL tcp {addr}:{port} {type(e).__name__}: {e}")
        return
    print(f"UNEXPECTED_SUCCESS tcp {addr}:{port}")
    fail = 1
tcp(socket.AF_INET, "1.1.1.1", 443)
tcp(socket.AF_INET, "8.8.8.8", 53)
tcp(socket.AF_INET6, "2606:4700:4700::1111", 443)
if gh:
    tcp(socket.AF_INET, gh, 443)
try:
    socket.getaddrinfo("example.com", 443)
    print("UNEXPECTED_SUCCESS dns example.com")
    fail = 1
except Exception as e:
    print(f"FAIL dns example.com {type(e).__name__}: {e}")
sys.exit(fail)
PY
  then
    docker rm -f "$name" >/dev/null 2>&1 || true
    echo "容器探针出现成功连接，见 $out" | tee -a "$EVID/violations.txt"
    return 1
  fi
  docker rm -f "$name" >/dev/null 2>&1 || true
}

save_rules() {
  iptables-save > "$EVID/iptables-save.txt" 2>/dev/null || true
  ip6tables-save > "$EVID/ip6tables-save.txt" 2>/dev/null || true
  {
    echo "## netns route"
    ip netns exec hd-sut ip route || true
    ip netns exec hd-sut ip -6 route || true
    echo "## host ss :443"
    ss -tn state established '( dport = :443 )' || true
  } > "$EVID/routes-and-control-channel.txt" 2>/dev/null || true
}

move_sut_cgroup() {
  local parent cg
  parent=$(ps -o ppid= -p $$ | tr -d ' ')
  cg=$(awk -F: '$1==0 {print $3; exit}' "/proc/${parent}/cgroup")
  if [ -z "$cg" ] || [ "$cg" = "/" ]; then
    echo "无法定位 runner 控制通道 cgroup（parent=$parent）" | tee -a "$EVID/violations.txt"
    return 1
  fi
  printf '%s\n' "$cg" > "$EVID/runner-cgroup.txt"
  mkdir -p /sys/fs/cgroup/hd-sut
  if ! echo $$ > /sys/fs/cgroup/hd-sut/cgroup.procs; then
    echo "无法把安装脚本挪进 hd-sut cgroup" | tee -a "$EVID/violations.txt"
    return 1
  fi
  RUNNER_CGROUP="$cg"
  echo "script_pid=$$ runner_cgroup=$cg sut_cgroup=/hd-sut" | tee "$EVID/cgroup-move.txt"
}

probe_escaped() {
  local label="$1" dest="$2"
  # 调用方已经在 hd-sut cgroup。nsenter 只换网络命名空间，不换 cgroup。
  if ! nsenter -t 1 -n python3 - "$label" > "$dest" << 'PY'
import os, socket, sys
label = sys.argv[1]
print("label", label)
print("uid", os.getuid())
fail = 0
def tcp(addr, port, family=socket.AF_INET):
    global fail
    s = socket.socket(family, socket.SOCK_STREAM)
    s.settimeout(3)
    try:
        s.connect((addr, port))
    except Exception as e:
        print(f"FAIL tcp {addr}:{port} {type(e).__name__}: {e}")
        return
    print(f"UNEXPECTED_SUCCESS tcp {addr}:{port}")
    fail = 1
tcp("1.1.1.1", 443)
tcp("2606:4700:4700::1111", 443, socket.AF_INET6)
try:
    socket.getaddrinfo("example.com", 443)
    print("UNEXPECTED_SUCCESS dns example.com")
    fail = 1
except Exception as e:
    print(f"FAIL dns example.com {type(e).__name__}: {e}")
sys.exit(fail)
PY
  then
    echo "逃出 netns 后仍能访问公网：$label" | tee -a "$EVID/violations.txt"
    return 1
  fi
}

teardown() {
  need_root
  iptables -D OUTPUT -j HD_OUT4 2>/dev/null || true
  ip6tables -D OUTPUT -j HD_OUT6 2>/dev/null || true
  iptables -F HD_OUT4 2>/dev/null || true
  iptables -X HD_OUT4 2>/dev/null || true
  ip6tables -F HD_OUT6 2>/dev/null || true
  ip6tables -X HD_OUT6 2>/dev/null || true
  iptables -D OUTPUT -p udp --dport 53 -j HD_DNS 2>/dev/null || true
  iptables -D OUTPUT -p tcp --dport 53 -j HD_DNS 2>/dev/null || true
  if [ -f "$STATE" ]; then
    # shellcheck disable=SC1090
    . "$STATE"
  fi
  if [ -n "${DOCKERD_UID:-}" ]; then
    iptables -D OUTPUT -m owner --uid-owner "$DOCKERD_UID" -j HD_ROOT 2>/dev/null || true
  fi
  iptables -D DOCKER-USER -j HD_DOCK4 2>/dev/null || true
  ip6tables -D DOCKER-USER -j HD_DOCK6 2>/dev/null || true
  iptables -D FORWARD -i veth-hd-host -j HD_VETH4 2>/dev/null || true
  ip6tables -D FORWARD -i veth-hd-host -j HD_VETH6 2>/dev/null || true
  iptables -D INPUT -i veth-hd-host -p tcp --dport 8080 -j ACCEPT 2>/dev/null || true
  iptables -D INPUT -i veth-hd-host -j REJECT 2>/dev/null || true
  ip6tables -t raw -D PREROUTING -i veth-hd-host ! -p ipv6-icmp -j HD_V6RAW 2>/dev/null || true
  ip6tables -t raw -F HD_V6RAW 2>/dev/null || true
  ip6tables -t raw -X HD_V6RAW 2>/dev/null || true
  ip6tables -D INPUT -i veth-hd-host -p icmpv6 -j ACCEPT 2>/dev/null || true
  ip6tables -D INPUT -i veth-hd-host -j REJECT 2>/dev/null || true
  for c in HD_VETH4 HD_DOCK4 HD_ROOT HD_DNS; do iptables -F "$c" 2>/dev/null || true; iptables -X "$c" 2>/dev/null || true; done
  for c in HD_VETH6 HD_DOCK6; do ip6tables -F "$c" 2>/dev/null || true; ip6tables -X "$c" 2>/dev/null || true; done
  ip netns del hd-sut 2>/dev/null || true
  ip link del veth-hd-host 2>/dev/null || true
  if [ -f /etc/docker/daemon.json.d50-bak ]; then
    mv /etc/docker/daemon.json.d50-bak /etc/docker/daemon.json
    systemctl restart docker || true
  elif [ "${DAEMON_BAK:-0}" = 0 ] && [ -f /etc/docker/daemon.json ]; then
    rm -f /etc/docker/daemon.json
    systemctl restart docker || true
  fi
  rm -f "$STATE"
  log "已拆除 D-50 隔离规则并恢复 docker 配置"
}

run_all() {
  need_root
  local tar="" evid=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --tar) tar="$2"; shift ;;
      --evidence) evid="$2"; shift ;;
      *) echo "未知参数 $1" >&2; exit 2 ;;
    esac
    shift
  done
  [ -n "$tar" ] && [ -f "$tar" ] || { echo "需要 --tar" >&2; exit 2; }
  [ -n "$evid" ] || evid=/tmp/d50-evidence
  EVID="$evid"
  mkdir -p "$EVID"
  chmod 777 "$EVID"
  : > "$EVID/violations.txt"
  rm -f "$STATE"
  umask 022

  local pw="${D50_ADMIN_PASSWORD:-ci-only-password-123}"
  export D50_ADMIN_PASSWORD="$pw"

  {
    echo "git_sha=${GITHUB_SHA:-unknown}"
    echo "date_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    uname -a
    echo "arch=$(uname -m)"
    free -h || true
    df -h || true
    docker version || true
    docker compose version || true
    echo "SUDO_UID=${SUDO_UID:-}"
    echo "SUDO_USER=${SUDO_USER:-}"
    echo "claim=github_hosted_x86_vm_only"
    echo "not_physical_machine=true"
    echo "not_customer_site=true"
  } > "$EVID/host.txt"
  uname -m | grep -qx x86_64

  sha256sum "$tar" | tee "$EVID/package.sha256"
  if tar -tzf "$tar" | grep -q 'hengdesk-mocks'; then
    echo "正式包含 mocks 镜像，拒绝" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  tar -tzf "$tar" > "$EVID/package-files.txt"
  log "准备阶段结束。此后禁止构建和拉取。产品镜像只能从包内 load。"

  record_docker before-clean
  clean_runtime
  enable_docker_ipv6
  clean_runtime
  record_docker baseline
  assert_clean_baseline
  {
    echo "images=$(docker images -q | wc -l)"
    echo "volumes=$(docker volume ls -q | wc -l)"
    echo "containers=$(docker ps -aq | wc -l)"
    docker images -q
    docker volume ls -q
    docker ps -aq
  } > "$EVID/baseline-empty.txt"
  if [ "$(docker images -q | wc -l)" != 0 ] || [ "$(docker volume ls -q | wc -l)" != 0 ] || [ "$(docker ps -aq | wc -l)" != 0 ]; then
    echo "清洁基线仍有镜像、卷或容器" | tee -a "$EVID/violations.txt"
    exit 1
  fi

  local runner_uid="${SUDO_UID:-}"
  if [ -z "$runner_uid" ] || [ "$runner_uid" = 0 ]; then
    echo "无法确定非 root 的 runner uid，不能在保留控制通道的同时证明安装进程用不上它" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  local dockerd_uid
  dockerd_uid=$(ps -o uid= -C dockerd | awk 'NR==1{print $1}')
  if [ -z "$dockerd_uid" ]; then
    echo "找不到 dockerd" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  echo "runner_uid=$runner_uid dockerd_uid=$dockerd_uid" | tee "$EVID/uids.txt"
  if [ "$runner_uid" = "$dockerd_uid" ]; then
    echo "runner 与 dockerd 同一 uid，无法把控制通道和 daemon 出站分开。本次不能记为 D-50 通过。" | tee -a "$EVID/violations.txt"
    exit 1
  fi

  move_sut_cgroup
  setup_netns
  setup_filters "$runner_uid" "$dockerd_uid"
  save_rules
  {
    echo "## v4"
    ip netns exec hd-sut ip route
    echo "## v6"
    ip netns exec hd-sut ip -6 route
    echo "## veth forwarding"
    sysctl net.ipv6.conf.veth-hd-host.forwarding
  } > "$EVID/netns-route.txt"
  ip netns identify $$ > "$EVID/installer-netns-before.txt" || true

  local gh_ip
  gh_ip=$(ss -tn state established '( dport = :443 )' | awk 'NR>1{print $5}' | sed 's/:[0-9]*$//' | grep -E '^[0-9.]+$' | grep -vE '^(10\.|127\.|172\.(1[6-9]|2[0-9]|3[0-1])\.|192\.168\.)' | head -1 || true)
  echo "control_channel_sample_ip=${gh_ip:-none}" | tee "$EVID/control-channel-ip.txt"

  snapshot_counters "$EVID/counters-before-netns-probe.txt"
  if ! probe_netns "${gh_ip:-}"; then
    echo "宿主侧 netns 探针未能全部失败" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  snapshot_counters "$EVID/counters-after-netns-probe.txt"
  assert_increase "$EVID/counters-before-netns-probe.txt" "$EVID/counters-after-netns-probe.txt" "iptables HD_VETH4"
  assert_increase "$EVID/counters-before-netns-probe.txt" "$EVID/counters-after-netns-probe.txt" "ip6tables-raw HD_V6RAW"
  # 即使提权回到 init 网络命名空间，hd-sut cgroup 也不能借用控制通道。
  snapshot_counters "$EVID/counters-before-escape.txt"
  probe_escaped root "$EVID/probe-escape-root.txt"
  sudo -u "${SUDO_USER:-runner}" /usr/bin/nsenter -t 1 -n /usr/bin/python3 - runner > "$EVID/probe-escape-runner-user.txt" 2>&1 << 'PY' || true
import os, socket, sys
print("uid", os.getuid())
fail = 0
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.settimeout(3)
try:
    s.connect(("1.1.1.1", 443))
    print("UNEXPECTED_SUCCESS tcp 1.1.1.1:443")
    fail = 1
except Exception as e:
    print(f"FAIL tcp 1.1.1.1:443 {type(e).__name__}: {e}")
try:
    socket.getaddrinfo("example.com", 443)
    print("UNEXPECTED_SUCCESS dns")
    fail = 1
except Exception as e:
    print(f"FAIL dns {type(e).__name__}: {e}")
sys.exit(fail)
PY
  if grep -q UNEXPECTED_SUCCESS "$EVID/probe-escape-runner-user.txt"; then
    echo "runner uid 在安装 cgroup 里仍能出网" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  # nsenter 被拒绝（Permission denied）或连接被拒绝都说明没有借到控制通道。
  # 只有明确连上才失败。空输出仍视为探针没跑。
  if [ ! -s "$EVID/probe-escape-runner-user.txt" ]; then
    echo "runner uid 逃逸探针没有输出" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  snapshot_counters "$EVID/counters-after-escape.txt"
  assert_increase "$EVID/counters-before-escape.txt" "$EVID/counters-after-escape.txt" "iptables HD_OUT4"

  log "隔离已生效。开始从正式包 load 并安装。"
  snapshot_counters "$EVID/counters-before-install.txt"
  local extract=/tmp/hd-d50-extract
  rm -rf "$extract"
  mkdir -p "$extract"
  tar -C "$extract" -xzf "$tar"
  local pkg
  pkg=$(find "$extract" -mindepth 1 -maxdepth 1 -type d | head -1)
  local ver
  ver=$(tr -d ' \n' < "$pkg/VERSION")
  echo "version=$ver" | tee "$EVID/package-version.txt"
  # 安装进程的网络命名空间必须是 hd-sut。
  ip netns exec hd-sut bash -c 'echo netns=$(ip netns identify $$ || true); ip route; echo ---; ip -6 route' > "$EVID/install-process-net.txt"
  if ! grep -q 'default via 169.254.250.1' "$EVID/install-process-net.txt"; then
    echo "安装进程不在隔离 netns 的默认路由上" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  (
    cd "$pkg/deploy"
    if ! command -v capsh >/dev/null; then
      echo "缺少 capsh，无法去掉安装进程的 CAP_SYS_ADMIN" | tee -a "$EVID/violations.txt"
      exit 1
    fi
    ip netns exec hd-sut capsh --keep=1 \
      --drop=cap_sys_admin,cap_sys_ptrace,cap_net_admin,cap_sys_module,cap_bpf -- \
      -c "unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy
        {
          echo cgroup=\$(cat /proc/self/cgroup)
          echo netns=\$(readlink /proc/self/ns/net)
          grep -E '^Cap(Eff|Bnd):' /proc/self/status
          if nsenter -t 1 -n true; then echo ESCAPE=allowed; exit 1; fi
          echo ESCAPE=denied
        } > \"$EVID/install-identity.txt\"
        ./install.sh --non-interactive --ack-master-key --offline --version '$ver' --storage seaweedfs --admin-password \"\$D50_ADMIN_PASSWORD\" --force --up"
  ) > "$EVID/install.log" 2>&1 || {
    echo "install.sh 失败，见 install.log" | tee -a "$EVID/violations.txt"
    exit 1
  }
  # install.log 不应出现主密钥或数据库口令全文。这里只检查管理员口令没有被脚本回显。
  if grep -F -q "$pw" "$EVID/install.log"; then
    echo "安装日志回显了管理员口令" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  grep -E '^(COMPOSE_FILE|HENGDESK_VERSION|HENGDESK_HTTP_PORT)=' "$pkg/deploy/.env" > "$EVID/install-env-public.txt"
  if grep -q acceptance "$EVID/install-env-public.txt"; then
    echo "安装使用了 acceptance compose" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  reassert_docker_user
  record_docker after-load
  if ! grep -q "hengdesk/server" "$EVID/docker-after-load.txt"; then
    echo "隔离后未见从包内加载的 server 镜像" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  if grep -q 'hengdesk/mocks' "$EVID/docker-after-load.txt"; then
    echo "加载了 mocks 镜像" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  grep -E 'Loaded image' "$EVID/install.log" > "$EVID/loaded-images.txt" || true
  if ! grep -q 'Loaded image' "$EVID/loaded-images.txt"; then
    echo "安装日志没有 docker load 的 Loaded image 记录" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  if grep -E 'docker pull|Pulling from|Downloaded newer image' "$EVID/install.log"; then
    echo "隔离后的安装进程出现了镜像拉取" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  if ! grep -q 'ESCAPE=denied' "$EVID/install-identity.txt"; then
    echo "安装进程仍能 nsenter 回 init 网络命名空间" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  snapshot_counters "$EVID/counters-after-install.txt"
  record_blocks "$EVID/counters-before-install.txt" "$EVID/counters-after-install.txt" "安装"

  # 验收进程留在 hd-sut cgroup（不能用 runner 控制通道），通过宿主回环上的发布端口访问产品。
  # veth 地址上的 8080 会被本机拒绝，不作为健康检查入口。
  local base=http://127.0.0.1:8080
  local accept="${GITHUB_WORKSPACE:-/workspace}/tests/acceptance/offline-formal-accept.sh"
  [ -f "$accept" ] || { echo "找不到离线验收脚本 $accept" | tee -a "$EVID/violations.txt"; exit 1; }
  chmod +x "$accept"
  env D50_ADMIN_PASSWORD="$pw" "$accept" init --base-url "$base" \
    > "$EVID/accept-init.log" 2>&1 || { echo "离线验收 init 失败" | tee -a "$EVID/violations.txt"; exit 1; }

  snapshot_counters "$EVID/counters-before-container-probe.txt"
  probe_container "hengdesk/web:$ver" hengdesk_default "${gh_ip:-}" "$EVID/probe-container.txt" d50-probe-v4 || exit 1
  docker network create --ipv6 --subnet fd00:50:2::/64 d50probe >> "$EVID/probe-ipv6-net.txt" 2>&1 || {
    echo "无法创建 IPv6 探针网络" | tee -a "$EVID/violations.txt"
    exit 1
  }
  probe_container "hengdesk/web:$ver" d50probe "${gh_ip:-}" "$EVID/probe-container-ipv6net.txt" d50-probe-v6 || exit 1
  # 宿主网络栈上的容器（--network host）也不在 runner cgroup，公网 IPv4/IPv6/DNS 必须失败。
  docker rm -f d50-probe-host >/dev/null 2>&1 || true
  docker run --rm --network host --entrypoint /bin/sh "hengdesk/web:$ver" -c '
    set +e
    wget -T 3 -O /dev/null http://1.1.1.1/; echo ipv4:$?
    wget -T 3 -O /dev/null "http://[2606:4700:4700::1111]/"; echo ipv6:$?
    wget -T 3 -O /dev/null http://example.com/; echo dns:$?
  ' > "$EVID/probe-hostnet.txt" 2>&1 || true
  if grep -E '^(ipv4|ipv6|dns):0$' "$EVID/probe-hostnet.txt"; then
    echo "host 网络容器探针成功出网" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  snapshot_counters "$EVID/counters-after-container-probe.txt"
  assert_increase "$EVID/counters-before-container-probe.txt" "$EVID/counters-after-container-probe.txt" "iptables HD_DOCK4"
  assert_increase "$EVID/counters-before-container-probe.txt" "$EVID/counters-after-container-probe.txt" "ip6tables HD_DOCK6"

  snapshot_counters "$EVID/counters-before-persist.txt"
  sleep 2
  (
    cd "$pkg/deploy"
    docker compose restart
  ) > "$EVID/restart.log" 2>&1
  env D50_ADMIN_PASSWORD="$pw" "$accept" persist --base-url "$base" \
    > "$EVID/accept-persist.log" 2>&1 || { echo "重启持久性失败" | tee -a "$EVID/violations.txt"; exit 1; }
  (
    cd "$pkg/deploy"
    docker compose exec -T postgres sh -c 'psql "postgresql://$POSTGRES_USER:$POSTGRES_PASSWORD@127.0.0.1/$POSTGRES_DB" -c "select count(*) as users from users"'
  ) > "$EVID/db-users.txt" 2>&1 || { echo "无法查询新库 users" | tee -a "$EVID/violations.txt"; exit 1; }
  if grep -F -q "$pw" "$EVID/db-users.txt"; then
    echo "数据库查询输出含管理员口令" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  (
    cd "$pkg/deploy"
    docker compose exec -T postgres sh -c 'psql "postgresql://$POSTGRES_USER:$POSTGRES_PASSWORD@127.0.0.1/$POSTGRES_DB" -tAc "select name from departments where name = '\''d50-marker'\''"'
  ) > "$EVID/db-marker.txt" 2>&1 || { echo "无法查询标记部门" | tee -a "$EVID/violations.txt"; exit 1; }
  if ! grep -qx 'd50-marker' "$EVID/db-marker.txt"; then
    echo "重启后数据库里没有标记部门" | tee -a "$EVID/violations.txt"
    exit 1
  fi
  snapshot_counters "$EVID/counters-after-persist.txt"
  record_blocks "$EVID/counters-before-persist.txt" "$EVID/counters-after-persist.txt" "验收与重启"

  save_rules
  docker network inspect hengdesk_default > "$EVID/network-hengdesk_default.json" || true
  if [ -s "$EVID/violations.txt" ]; then
    echo "result=fail" | tee "$EVID/result.txt"
    exit 1
  fi
  {
    echo "result=pass"
    echo "scope=github_hosted_ubuntu_24_04_x86_64_vm"
    echo "not_physical=true"
    echo "not_customer_site=true"
    echo "package_sha256=$(cut -d' ' -f1 "$EVID/package.sha256")"
    echo "version=$ver"
    echo "control_channel=runner cgroup $(cat "$EVID/runner-cgroup.txt") established sockets plus new flows from that cgroup only"
    echo "sut=cgroup hd-sut + netns hd-sut + caps dropped + docker forward + host-net containers rejected by HD_OUT"
  } | tee "$EVID/result.txt"
  chmod -R a+rX "$EVID" || true
  log "D-50 云 VM 证据收集完成。拆除隔离后再由控制通道上传。"
}

case "$CMD" in
  run)
    trap 'ec=$?; if [ "$ec" -ne 0 ]; then mkdir -p "${EVID:-/tmp/d50-evidence}"; echo "result=fail exit=$ec" >> "${EVID:-/tmp/d50-evidence}/result.txt"; fi; teardown || true; exit "$ec"' EXIT
    run_all "$@"
    ;;
  teardown) teardown ;;
  *) echo "用法：$0 run|teardown" >&2; exit 2 ;;
esac
