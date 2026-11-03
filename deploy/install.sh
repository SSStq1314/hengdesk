#!/usr/bin/env bash
# 衡台 HengDesk 安装向导：选择对象存储（seaweedfs / minio / external），生成 .env 并选定 compose 文件。
# 交互模式：./install.sh
# 非交互：./install.sh --non-interactive --storage seaweedfs|minio|external \
#   [--s3-endpoint URL --s3-region R --s3-access-key AK --s3-secret-key SK --s3-bucket B --s3-path-style true|false] \
#   [--admin-email E] [--admin-password P] [--http-port 8080] [--force] [--up]
set -euo pipefail
cd "$(dirname "$0")"

STORAGE="" INTERACTIVE=1 FORCE=0 UP=0
S3_ENDPOINT="" S3_REGION="us-east-1" S3_AK="" S3_SK="" S3_BUCKET="hengdesk" S3_PATH_STYLE="true"
ADMIN_EMAIL="admin@hengdesk.local" ADMIN_PASSWORD="" HTTP_PORT="8080"

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
    --http-port) HTTP_PORT="$2"; shift ;;
    --force) FORCE=1 ;;
    --up) UP=1 ;;
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

if [ -f .env ] && [ "$FORCE" != 1 ]; then
  [ "$INTERACTIVE" = 1 ] || die ".env 已存在，使用 --force 覆盖"
  ask ow ".env 已存在，是否覆盖（y/N）" "N"; [ "$ow" = y ] || [ "$ow" = Y ] || die "已取消"
fi

umask 077
cat > .env <<ENV
# 由 install.sh 于 $(date '+%Y-%m-%d %H:%M:%S %z') 生成，存储：$STORAGE
COMPOSE_FILE=$COMPOSE_FILE
HENGDESK_VERSION=dev
HENGDESK_HTTP_PORT=$HTTP_PORT
HENGDESK_ENV=prod
HENGDESK_COOKIE_SECURE=false
POSTGRES_USER=hengdesk
POSTGRES_PASSWORD=$(rand 32)
POSTGRES_DB=hengdesk
HENGDESK_JWT_SECRET=$(rand 48)
HENGDESK_ADMIN_EMAIL=$ADMIN_EMAIL
HENGDESK_ADMIN_PASSWORD=$ADMIN_PASSWORD
HENGDESK_S3_ENDPOINT=$S3_ENDPOINT
HENGDESK_S3_REGION=$S3_REGION
HENGDESK_S3_BUCKET=$S3_BUCKET
HENGDESK_S3_ACCESS_KEY=$S3_AK
HENGDESK_S3_SECRET_KEY=$S3_SK
HENGDESK_S3_PATH_STYLE=$S3_PATH_STYLE
ENV

echo "已生成 $(pwd)/.env（权限 600），compose 文件：$COMPOSE_FILE"
echo "管理员账号：$ADMIN_EMAIL"
if [ "$GENERATED_PW" = 1 ]; then echo "管理员密码已随机生成，查看：grep HENGDESK_ADMIN_PASSWORD $(pwd)/.env"; fi
if [ "$UP" = 1 ]; then docker compose up -d --build; else echo "启动：cd $(pwd) && docker compose up -d --build"; fi
