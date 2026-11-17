#!/usr/bin/env bash
# 从上游 GitHub Release 安装固定版本的 gitleaks 与 trivy，并核对 SHA256。
# 不读取仓库密钥，不使用浮动的 latest。
set -euo pipefail

GITLEAKS_VERSION=8.30.1
GITLEAKS_SHA256=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
TRIVY_VERSION=0.76.2
TRIVY_SHA256=bc0fc726e017b8927a62b2d3531d7a84ee01769daf5c99ed866dced54089fdbf

dest="${1:-/tmp/hd-scanners}"
mkdir -p "$dest"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fetch() {
  local url="$1" file="$2" want="$3"
  curl -fsSL -o "$tmp/$file" "$url"
  echo "$want  $tmp/$file" | sha256sum -c -
}

fetch "https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_linux_x64.tar.gz" \
  gitleaks.tgz "$GITLEAKS_SHA256"
tar -xzf "$tmp/gitleaks.tgz" -C "$dest" gitleaks
chmod 755 "$dest/gitleaks"

fetch "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-64bit.tar.gz" \
  trivy.tgz "$TRIVY_SHA256"
tar -xzf "$tmp/trivy.tgz" -C "$dest" trivy
chmod 755 "$dest/trivy"

"$dest/gitleaks" version
"$dest/trivy" --version
echo "scanners_dir=$dest"
