#!/usr/bin/env bash
# 从上游 GitHub Release 安装固定版本的 gitleaks 与 trivy，并核对 SHA256。
# 校验和与安装包来自同一 Release。这只证明字节与该 Release 上的 checksums 文件一致。
# 本脚本不验证 sigstore 或任何独立签名。
# 不读取仓库密钥，不使用浮动的 latest。下载失败或校验失败直接退出。
set -euo pipefail

GITLEAKS_VERSION=8.30.1
GITLEAKS_SHA256=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
TRIVY_VERSION=0.76.2
TRIVY_SHA256=bc0fc726e017b8927a62b2d3531d7a84ee01769daf5c99ed866dced54089fdbf

fetch() {
  local url="$1" file="$2" want="$3"
  curl -fsSL -o "$tmp/$file" "$url"
  echo "$want  $tmp/$file" | sha256sum -c -
}

self_test() {
  local work bad rc
  work=$(mktemp -d)
  tmp="$work"
  bad=0000000000000000000000000000000000000000000000000000000000000000
  printf 'hd-fixture-not-an-archive\n' > "$work/blob"
  set +e
  ( fetch "file://$work/blob" bad.tgz "$bad" ) >/tmp/hd-scanner-bad-sum.txt 2>&1
  rc=$?
  set -e
  if [ "$rc" -eq 0 ]; then
    echo "校验和不匹配时安装没有失败" >&2
    exit 1
  fi
  set +e
  ( fetch "file:///tmp/hd-scanner-missing-fixture" missing.tgz "$bad" ) >/tmp/hd-scanner-missing.txt 2>&1
  rc=$?
  set -e
  if [ "$rc" -eq 0 ]; then
    echo "安装包下载失败时没有阻断" >&2
    exit 1
  fi
  rm -rf "$work"
  echo "扫描器下载失败和校验失败都会阻断"
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  exit 0
fi

dest="${1:-/tmp/hd-scanners}"
mkdir -p "$dest"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

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
