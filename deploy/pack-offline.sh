#!/usr/bin/env bash
# 在有网构建机生成 x86_64 离线安装包（不提交到仓库）。
# 用法：./pack-offline.sh [--version VER] [--out DIR] [--with-minio]
# 输出：$OUT/hengdesk-offline-$VER-amd64.tar.gz （内含 deploy/、images/*.tar、VERSION、README）
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${HENGDESK_VERSION:-}"
OUT="$ROOT/dist"
WITH_MINIO=0
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift ;;
    --out) OUT="$2"; shift ;;
    --with-minio) WITH_MINIO=1 ;;
    -h|--help) sed -n '2,5p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数：$1" >&2; exit 1 ;;
  esac
  shift
done
[ -n "$VERSION" ] || VERSION="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || date +%Y%m%d%H%M)"
ARCH=$(uname -m)
case "$ARCH" in x86_64|amd64) ;; *) echo "请在 x86_64 上打包" >&2; exit 1 ;; esac

STAGE="$OUT/stage-hengdesk-offline-$VERSION-amd64"
rm -rf "$STAGE"
mkdir -p "$STAGE/deploy" "$STAGE/images"
echo "$VERSION" > "$STAGE/VERSION"

# 构建应用镜像
echo "==> build hengdesk/server:$VERSION / hengdesk/web:$VERSION"
docker build -t "hengdesk/server:$VERSION" "$ROOT/server"
docker build -t "hengdesk/web:$VERSION" "$ROOT/web"

# 依赖镜像（与 docker-compose.yml 钉死版本一致）
DEPS=(
  "pgvector/pgvector:0.8.7-pg17"
  "valkey/valkey:8.1.11-alpine"
  "chrislusf/seaweedfs:4.52"
)
if [ "$WITH_MINIO" = 1 ]; then
  # Chainguard MinIO（可选，AGPL，见 D-28）
  DEPS+=("cgr.dev/chainguard/minio:latest")
fi

echo "==> pull 依赖镜像"
for img in "${DEPS[@]}"; do
  docker pull "$img"
done

echo "==> docker save"
docker save -o "$STAGE/images/hengdesk-server.tar" "hengdesk/server:$VERSION"
docker save -o "$STAGE/images/hengdesk-web.tar" "hengdesk/web:$VERSION"
docker save -o "$STAGE/images/deps.tar" "${DEPS[@]}"

# 复制部署文件
cp -a "$ROOT/deploy/." "$STAGE/deploy/"
# 离线包内 .env.example 默认版本
sed -i "s/^HENGDESK_VERSION=.*/HENGDESK_VERSION=$VERSION/" "$STAGE/deploy/.env.example" || true
# 去掉 build 上下文，强制用已 load 的 image（compose 仍可读 build 字段，安装时用 --no-build）
cat > "$STAGE/README.md" <<MD
# 衡台 HengDesk 离线安装包（$VERSION · amd64）

## 安装
1. 解压本包到目标机（需 Docker + Compose v2，x86_64，建议 ≥20 GiB 可用磁盘）。
2. \`cd deploy && ./offline-check.sh\`
3. \`./install.sh --non-interactive --ack-master-key --offline --storage seaweedfs --admin-password '你的密码' --up\`
   或交互：\`./install.sh --offline --up\`
4. 浏览器打开 \`http://<主机>:8080/\`

## 升级
1. 解压新包，\`docker load -i images/*.tar\`
2. 将 \`deploy/.env\` 中 \`HENGDESK_VERSION\` 改为 \`$VERSION\`
3. \`./upgrade.sh\`（不要加 \`--build\`）

## 卸载
\`./uninstall.sh\`（加 \`--volumes\` 才删数据卷，须交互确认或非交互加 \`--yes\`；请先备份；secrets/ 始终保留）
MD

TAR="$OUT/hengdesk-offline-$VERSION-amd64.tar.gz"
mkdir -p "$OUT"
tar -C "$(dirname "$STAGE")" -czf "$TAR" "$(basename "$STAGE")"
rm -rf "$STAGE"
echo "已生成：$TAR"
ls -lh "$TAR"
