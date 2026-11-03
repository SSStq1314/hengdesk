#!/usr/bin/env bash
# 衡台 HengDesk 安装向导：选择对象存储（seaweedfs / minio / external），生成 .env 并选定 compose 文件。
# 交互模式：./install.sh
# 非交互：./install.sh --non-interactive --storage seaweedfs|minio|external \
#   [--s3-endpoint URL --s3-region R --s3-access-key AK --s3-secret-key SK --s3-bucket B --s3-path-style true|false] \
#   [--admin-email E] [--admin-password P] [--auditor-email E [--auditor-password P]] [--http-port 8080] \
#   [--tls-cert 证书.pem --tls-key 私钥.pem | --self-signed [--san 域名或IP ...]] [--https-port 443] [--redirect-port 80] [--no-http-redirect] [--force] [--up] [--ack-master-key]
set -euo pipefail
cd "$(dirname "$0")"

STORAGE="" INTERACTIVE=1 FORCE=0 UP=0
S3_ENDPOINT="" S3_REGION="us-east-1" S3_AK="" S3_SK="" S3_BUCKET="hengdesk" S3_PATH_STYLE="true"
ADMIN_EMAIL="admin@hengdesk.local" ADMIN_PASSWORD="" HTTP_PORT="8080"
AUDITOR_EMAIL="" AUDITOR_PASSWORD=""
TLS_CERT="" TLS_KEY=""
SELF_SIGNED=0 SANS=() SAN_LIST=""
HTTPS_PORT="443" REDIRECT_PORT="80" HTTP_REDIRECT="true"
ACK_MASTER_KEY=0

usage() { sed -n '2,7p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }
die() { echo "错误：$*" >&2; exit 1; }
rand() { local s=""; while [ "${#s}" -lt "$1" ]; do s+=$(head -c 64 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9'); done; printf '%s' "${s:0:$1}"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --non-interactive|-y) INTERACTIVE=0 ;;
    --storage) STORAGE="$2"; shift ;;
    --s3-endpoint) S3_ENDPOINT="$2"; shift ;;
    --s3-region) S3_REGION="$2"; shift ;;
    --s3-access-key) S3_AK="$2"; shift ;;
    --s3-secret-key) S3_SK="$2"; shift ;;
    --s3-bucket) S3_BUCKET="$2"; shift ;;
    --s3-path-style) S3_PATH_STYLE="$2"; shift ;;
    --admin-email) ADMIN_EMAIL="$2"; shift ;;
    --admin-password) ADMIN_PASSWORD="$2"; shift ;;
    --auditor-email) AUDITOR_EMAIL="$2"; shift ;;
    --auditor-password) AUDITOR_PASSWORD="$2"; shift ;;
    --http-port) HTTP_PORT="$2"; shift ;;
    --tls-cert) TLS_CERT="$2"; shift ;;
    --tls-key) TLS_KEY="$2"; shift ;;
    --self-signed) SELF_SIGNED=1 ;;
    --san) SANS+=("$2"); shift ;;
    --https-port) HTTPS_PORT="$2"; shift ;;
    --redirect-port) REDIRECT_PORT="$2"; shift ;;
    --no-http-redirect) HTTP_REDIRECT="false" ;;
    --force) FORCE=1 ;;
    --up) UP=1 ;;
    --ack-master-key) ACK_MASTER_KEY=1 ;;
    -h|--help) usage 0 ;;
    *) echo "未知参数：$1" >&2; usage 1 ;;
  esac
  shift
done

ask() { # ask 变量名 提示 默认值 [secret]
  local __v="$1" prompt="$2" def="$3" ans
  if [ "${4:-}" = secret ]; then read -r -s -p "$prompt: " ans; echo; else read -r -p "$prompt [$def]: " ans; fi
  printf -v "$__v" '%s' "${ans:-$def}"
}

if [ "$INTERACTIVE" = 1 ]; then
  echo "== 衡台 HengDesk 安装向导 =="
  echo "对象存储：1) SeaweedFS（默认，Apache-2.0）  2) MinIO（AGPL-3.0，可选）  3) 外部 S3"
  ask choice "请选择" "1"
  case "$choice" in 1|seaweedfs) STORAGE=seaweedfs ;; 2|minio) STORAGE=minio ;; 3|external) STORAGE=external ;; *) die "无效选择" ;; esac
  if [ "$STORAGE" = external ]; then
    ask S3_ENDPOINT "S3 地址（如 https://s3.example.com）" ""
    ask S3_REGION "Region" "$S3_REGION"
    ask S3_BUCKET "Bucket" "$S3_BUCKET"
    ask S3_PATH_STYLE "使用 path-style（true/false）" "$S3_PATH_STYLE"
    ask S3_AK "Access Key" ""
    ask S3_SK "Secret Key（输入不回显）" "" secret
  fi
  ask HTTP_PORT "Web 端口" "$HTTP_PORT"
  ask ADMIN_EMAIL "管理员邮箱" "$ADMIN_EMAIL"
  ask ADMIN_PASSWORD "管理员密码（留空自动生成，不回显）" "" secret
  ask AUDITOR_EMAIL "初始安全审计员邮箱（三员分立，留空跳过）" ""
  if [ -n "$AUDITOR_EMAIL" ]; then ask AUDITOR_PASSWORD "审计员密码（留空自动生成，不回显）" "" secret; fi
  ask TLS_CERT "HTTPS 证书路径（PEM，含中间证书；输入 self-signed 生成自签证书；留空则仅 HTTP，由前置负载均衡处理 TLS）" ""
  if [ "$TLS_CERT" = self-signed ]; then
    TLS_CERT="" SELF_SIGNED=1
    ask SAN_LIST "证书域名或 IP（空格分隔，留空为 localhost 与本机主机名）" ""
    [ -n "$SAN_LIST" ] && read -r -a SANS <<<"$SAN_LIST"
  fi
  if [ -n "$TLS_CERT" ] || [ "$SELF_SIGNED" = 1 ]; then
    [ "$SELF_SIGNED" = 1 ] || ask TLS_KEY "私钥路径（PEM）" ""
    ask HTTPS_PORT "HTTPS 端口" "$HTTPS_PORT"
    ask HTTP_REDIRECT "HTTP 是否跳转 HTTPS（true/false）" "$HTTP_REDIRECT"
    if [ "$HTTP_REDIRECT" = true ]; then ask REDIRECT_PORT "跳转用的 HTTP 端口" "$REDIRECT_PORT"; fi
  fi
fi

port_ok() { case "$2" in ''|*[!0-9]*) die "$1 必须是 1-65535 的数字：$2" ;; esac; [ "$2" -ge 1 ] && [ "$2" -le 65535 ] || die "$1 必须是 1-65535 的数字：$2"; }
port_ok --http-port "$HTTP_PORT"; port_ok --https-port "$HTTPS_PORT"; port_ok --redirect-port "$REDIRECT_PORT"
case "$HTTP_REDIRECT" in true|false) ;; *) die "HTTP 跳转只能是 true / false" ;; esac

# 可选生成自签证书（D-34 第 3 点）：有效期 365 天，默认不生成；SAN 取 --san（可多次），缺省为 localhost 与本机主机名。
if [ "$SELF_SIGNED" = 1 ]; then
  [ -z "$TLS_CERT" ] && [ -z "$TLS_KEY" ] || die "--self-signed 不能与 --tls-cert / --tls-key 同时使用"
  command -v openssl >/dev/null 2>&1 || die "生成自签证书需要 openssl"
  if [ "${#SANS[@]}" -eq 0 ]; then SANS=(localhost); h="$(hostname -f 2>/dev/null || hostname)"; [ -n "$h" ] && [ "$h" != localhost ] && SANS+=("$h"); fi
  SAN_EXT=""
  for s in "${SANS[@]}"; do
    case "$s" in ''|*[!A-Za-z0-9.:*-]*) die "--san 只能是域名或 IP：$s" ;; esac
    if printf '%s' "$s" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}$|:'; then SAN_EXT+="${SAN_EXT:+,}IP:$s"; else SAN_EXT+="${SAN_EXT:+,}DNS:$s"; fi
  done
  SS_DIR="$(mktemp -d)"; trap 'rm -rf "$SS_DIR"' EXIT
  ( umask 077; openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 365 -subj "/CN=${SANS[0]}/O=HengDesk self-signed" \
      -addext "subjectAltName=$SAN_EXT" -addext "basicConstraints=critical,CA:FALSE" \
      -addext "keyUsage=critical,digitalSignature,keyEncipherment" -addext "extendedKeyUsage=serverAuth" \
      -keyout "$SS_DIR/tls.key" -out "$SS_DIR/tls.crt" >/dev/null 2>&1 ) || die "自签证书生成失败"
  TLS_CERT="$SS_DIR/tls.crt" TLS_KEY="$SS_DIR/tls.key"
  echo "已生成自签证书（365 天）：$SAN_EXT"
fi

# 可选导入 TLS 证书（D-30）：校验后复制到 ./tls，nginx 启用 443，80/8080 跳转 443，会话 cookie 打开 Secure。
TLS=0
if [ -n "$TLS_CERT" ] || [ -n "$TLS_KEY" ]; then
  [ -n "$TLS_CERT" ] && [ -n "$TLS_KEY" ] || die "--tls-cert 与 --tls-key 必须同时提供"
  [ -r "$TLS_CERT" ] || die "证书不可读：$TLS_CERT"
  [ -r "$TLS_KEY" ] || die "私钥不可读：$TLS_KEY"
  if command -v openssl >/dev/null 2>&1; then
    openssl x509 -in "$TLS_CERT" -noout 2>/dev/null || die "证书不是有效的 PEM：$TLS_CERT"
    [ "$(openssl x509 -in "$TLS_CERT" -noout -pubkey 2>/dev/null | openssl sha256)" = \
      "$(openssl pkey -in "$TLS_KEY" -pubout 2>/dev/null | openssl sha256)" ] || die "证书与私钥不匹配"
    openssl x509 -in "$TLS_CERT" -noout -checkend 0 >/dev/null || echo "警告：证书已过期" >&2
  fi
  TLS=1
fi

STORAGE="${STORAGE:-seaweedfs}"
case "$STORAGE" in
  seaweedfs) COMPOSE_FILE=docker-compose.yml; S3_ENDPOINT=http://seaweedfs:8333; S3_PATH_STYLE=true ;;
  minio) COMPOSE_FILE=docker-compose.yml:docker-compose.minio.yml; S3_ENDPOINT=http://minio:9000; S3_PATH_STYLE=true ;;
  external)
    COMPOSE_FILE=docker-compose.yml:docker-compose.external-s3.yml
    [ -n "$S3_ENDPOINT" ] && [ -n "$S3_AK" ] && [ -n "$S3_SK" ] || die "外部 S3 需要 --s3-endpoint / --s3-access-key / --s3-secret-key" ;;
  *) die "--storage 只能是 seaweedfs / minio / external" ;;
esac
if [ "$STORAGE" != external ]; then S3_AK="${S3_AK:-hd$(rand 18)}"; S3_SK="${S3_SK:-$(rand 40)}"; fi
GENERATED_PW=0
if [ -z "$ADMIN_PASSWORD" ]; then ADMIN_PASSWORD="$(rand 20)"; GENERATED_PW=1; fi

GENERATED_AUD_PW=0
if [ -n "$AUDITOR_EMAIL" ]; then
  [ "$(printf '%s' "$AUDITOR_EMAIL" | tr 'A-Z' 'a-z')" != "$(printf '%s' "$ADMIN_EMAIL" | tr 'A-Z' 'a-z')" ] || die "审计员不能与管理员为同一账号（三员分立）"
  if [ -z "$AUDITOR_PASSWORD" ]; then AUDITOR_PASSWORD="$(rand 20)"; GENERATED_AUD_PW=1; fi
fi

if [ -f .env ] && [ "$FORCE" != 1 ]; then
  [ "$INTERACTIVE" = 1 ] || die ".env 已存在，使用 --force 覆盖"
  ask ow ".env 已存在，是否覆盖（y/N）" "N"; [ "$ow" = y ] || [ "$ow" = Y ] || die "已取消"
fi

umask 077
COOKIE_SECURE=false PUBLIC_HTTPS=false
if [ "$TLS" = 1 ]; then
  mkdir -p tls && chmod 700 tls
  cp "$TLS_CERT" tls/tls.crt.new && cp "$TLS_KEY" tls/tls.key.new
  mv tls/tls.crt.new tls/tls.crt && mv tls/tls.key.new tls/tls.key
  chmod 644 tls/tls.crt && chmod 600 tls/tls.key
  COMPOSE_FILE="$COMPOSE_FILE:docker-compose.tls.yml"; COOKIE_SECURE=true; PUBLIC_HTTPS=true
  if [ "$HTTP_REDIRECT" = true ]; then
    [ "$REDIRECT_PORT" != "$HTTP_PORT" ] || die "--redirect-port 不能与 --http-port 相同"
    COMPOSE_FILE="$COMPOSE_FILE:docker-compose.tls-redirect.yml"
  fi
  [ "$HTTPS_PORT" != "$HTTP_PORT" ] && [ "$HTTPS_PORT" != "$REDIRECT_PORT" ] || die "--https-port 与其他端口冲突"
fi
# 模型网关主密钥（AES-256-GCM，加密上游 API Key）：只在不存在时生成，--force 重装也不会覆盖，
# 否则已保存的上游密钥将无法解密。
mkdir -p secrets && chmod 700 secrets
if [ ! -s secrets/master.key ]; then
  head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > secrets/master.key && chmod 600 secrets/master.key
fi
MASTER_KEY_PATH="$(pwd)/secrets/master.key"
echo "模型网关主密钥：$MASTER_KEY_PATH"
echo "！丢失后已存的 Key 无法恢复，请与数据库备份分开保管（数据库备份 /backups 不包含主密钥）。"
if [ "$INTERACTIVE" = 1 ]; then
  ask ack "已把主密钥复制到与数据库备份不同的位置？输入 yes 确认" ""
  [ "$ack" = yes ] || die "请先备份主密钥再继续安装（也可在管理后台「系统状态」稍后确认）"
elif [ "$ACK_MASTER_KEY" != 1 ]; then
  die "非交互安装需加 --ack-master-key，表示已知晓主密钥位置并会单独备份：$MASTER_KEY_PATH"
fi
cat > .env <<ENV
# 由 install.sh 于 $(date '+%Y-%m-%d %H:%M:%S %z') 生成，存储：$STORAGE
COMPOSE_FILE=$COMPOSE_FILE
HENGDESK_VERSION=dev
HENGDESK_HTTP_PORT=$HTTP_PORT
HENGDESK_HTTPS_PORT=$HTTPS_PORT
HENGDESK_REDIRECT_PORT=$REDIRECT_PORT
HENGDESK_HTTP_REDIRECT=$HTTP_REDIRECT
HENGDESK_TLS_SELF_SIGNED=$([ "$SELF_SIGNED" = 1 ] && echo true || echo false)
HENGDESK_ENV=prod
HENGDESK_COOKIE_SECURE=$COOKIE_SECURE
HENGDESK_PUBLIC_HTTPS=$PUBLIC_HTTPS
POSTGRES_USER=hengdesk
POSTGRES_PASSWORD=$(rand 32)
POSTGRES_DB=hengdesk
HENGDESK_ADMIN_EMAIL=$ADMIN_EMAIL
HENGDESK_ADMIN_PASSWORD=$ADMIN_PASSWORD
HENGDESK_AUDITOR_EMAIL=$AUDITOR_EMAIL
HENGDESK_AUDITOR_PASSWORD=$AUDITOR_PASSWORD
HENGDESK_S3_ENDPOINT=$S3_ENDPOINT
HENGDESK_S3_REGION=$S3_REGION
HENGDESK_S3_BUCKET=$S3_BUCKET
HENGDESK_S3_ACCESS_KEY=$S3_AK
HENGDESK_S3_SECRET_KEY=$S3_SK
HENGDESK_S3_PATH_STYLE=$S3_PATH_STYLE
HENGDESK_MASTER_KEY=$(cat secrets/master.key)
HENGDESK_MASTER_KEY_HOST_PATH=$MASTER_KEY_PATH
ENV

echo "已生成 $(pwd)/.env（权限 600），compose 文件：$COMPOSE_FILE"
if [ "$TLS" = 1 ]; then
  echo "HTTPS 已启用：证书已导入 $(pwd)/tls，访问 https://<主机>:$HTTPS_PORT/"
  if [ "$SELF_SIGNED" = 1 ]; then echo "注意：自签证书浏览器不信任，管理后台会常驻提示；客户端可导入 $(pwd)/tls/tls.crt 以信任。"; fi
  if [ "$HTTP_REDIRECT" = true ]; then echo "HTTP 端口 $REDIRECT_PORT 与 $HTTP_PORT 自动跳转 HTTPS"
  else echo "HTTP 跳转已关闭：不占用 $REDIRECT_PORT，$HTTP_PORT 只保留 /healthz"; fi
else echo "提示：未导入证书，仅 HTTP；请在前置负载均衡终止 TLS 并设置 HENGDESK_PUBLIC_HTTPS=true，否则管理后台会常驻提示。"; fi
echo "主密钥：$MASTER_KEY_PATH（管理后台「系统状态」会常驻提示，直到管理员确认已单独备份）"
echo "管理员账号：$ADMIN_EMAIL"
if [ "$GENERATED_PW" = 1 ]; then echo "管理员密码已随机生成，查看：grep HENGDESK_ADMIN_PASSWORD $(pwd)/.env"; fi
if [ -n "$AUDITOR_EMAIL" ]; then
  echo "初始安全审计员：$AUDITOR_EMAIL"
  if [ "$GENERATED_AUD_PW" = 1 ]; then echo "审计员密码已随机生成，查看：grep HENGDESK_AUDITOR_PASSWORD $(pwd)/.env"; fi
else
  echo "提示：未指派安全审计员，管理后台会常驻提示；可稍后在「角色」页面指派。"
fi
if [ "$UP" = 1 ]; then docker compose up -d --build; else echo "启动：cd $(pwd) && docker compose up -d --build"; fi
