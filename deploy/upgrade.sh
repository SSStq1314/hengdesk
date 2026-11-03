#!/usr/bin/env bash
# 衡台升级脚本：停写 → migrate（自动 pg_dump 备份，备份失败即中止，数据库不变）→ 启动新版本。
# 用法：./upgrade.sh [--build]
#   先把 .env 中 HENGDESK_VERSION 改为目标版本（离线包：先 docker load 新镜像）；源码部署加 --build。
# 迁移只向前执行；回滚 = 用本次备份执行 ./restore.sh（见 docs/ops/backup-restore.md）。
set -euo pipefail
cd "$(dirname "$0")"
[ -f .env ] || { echo "缺少 .env，请先运行 install.sh" >&2; exit 1; }
# 兼容旧 .env（D-34 第 2 点之前生成）：HTTPS 模式保持原行为，继续占用 80 并跳转。
if grep -q 'docker-compose.tls.yml' .env && ! grep -q '^HENGDESK_HTTP_REDIRECT=' .env; then
  sed -i 's/docker-compose.tls.yml/docker-compose.tls.yml:docker-compose.tls-redirect.yml/' .env
  printf 'HENGDESK_HTTP_REDIRECT=true\nHENGDESK_REDIRECT_PORT=80\n' >> .env
  echo "==> 已为旧 .env 补上 HTTP 跳转配置（保持 80 跳转 443）"
fi
# 兼容旧 .env（模型网关之前生成）：补上主密钥，已有 secrets/master.key 时沿用。
if ! grep -q '^HENGDESK_MASTER_KEY=' .env; then
  (umask 077; mkdir -p secrets
   [ -s secrets/master.key ] || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > secrets/master.key)
  printf 'HENGDESK_MASTER_KEY=%s\n' "$(cat secrets/master.key)" >> .env
  echo "==> 已生成模型网关主密钥 secrets/master.key 并写入 .env（请妥善保管）"
fi
# D-36：补上主密钥宿主机路径；主密钥轮换（keytool）后把 secrets/master.key 同步到 .env。
grep -q '^HENGDESK_MASTER_KEY_HOST_PATH=' .env || printf 'HENGDESK_MASTER_KEY_HOST_PATH=%s\n' "$(pwd)/secrets/master.key" >> .env
if [ -r secrets/master.key ]; then
  K=$(tr -d ' \n' < secrets/master.key)
  if [ -n "$K" ] && ! grep -qx "HENGDESK_MASTER_KEY=$K" .env; then
    sed -i "s/^HENGDESK_MASTER_KEY=.*/HENGDESK_MASTER_KEY=$K/" .env
    echo "==> 已把 secrets/master.key（轮换后的新主密钥）同步到 .env"
  fi
  unset K
fi
if [ "${1:-}" = "--build" ]; then docker compose build migrate; fi
echo "==> 停止 server / worker（停写，保证备份一致）"
docker compose stop server worker
echo "==> 执行迁移（有待执行迁移时先自动备份）"
if ! docker compose run --rm migrate; then
  echo "升级失败：若为备份失败，数据库未做任何改动。可执行 docker compose up -d 继续使用当前版本。" >&2
  exit 1
fi
echo "==> 启动服务"
docker compose up -d
echo "==> 现有备份（最新在前）："
docker compose exec -T postgres sh -c 'ls -1t /backups | head -5'
