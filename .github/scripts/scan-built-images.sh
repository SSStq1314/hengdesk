#!/usr/bin/env bash
# 扫描调用方刚刚构建或拉取的镜像，并把 SPDX 补注写进同一目录。
# 这些标签必须就是即将 docker save 的标签。不要拿另一次构建的报告来充数。
set -u
dest="${1:?用法: scan-built-images.sh DEST IMAGE...}"
shift
if [ "$#" -eq 0 ]; then
  echo "没有要扫描的镜像" >&2
  exit 1
fi
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
mkdir -p "$dest"
export PATH="${HD_SCANNERS:-/tmp/hd-scanners}:$PATH"
command -v trivy >/dev/null 2>&1 || { echo "缺少 trivy" >&2; exit 1; }
: > "$dest/errors.txt"
note() { printf 'SCAN_ERROR %s\n' "$*" | tee -a "$dest/errors.txt"; }
for img in "$@"; do
  safe=$(printf '%s' "$img" | tr '/:' '__')
  if ! docker image inspect "$img" >/dev/null 2>&1; then
    note "missing $img"
    continue
  fi
  if ! trivy image --quiet --scanners vuln --severity HIGH,CRITICAL \
    --format json --exit-code 0 --timeout 15m \
    --output "$dest/${safe}.json" "$img"; then
    note "trivy $img"
    continue
  fi
  python3 "$ROOT/.github/scripts/trivy-image-gate.py" "$dest/${safe}.json" || note "gate $img"
done
pg=""
for img in "$@"; do
  case "$img" in hengdesk/postgres:*) pg="$img" ;; esac
done
if [ -n "$pg" ]; then
  if trivy image --format spdx-json --output "$dest/hengdesk-postgres.spdx.json" "$pg"; then
    python3 "$ROOT/.github/scripts/annotate-postgres-sbom.py" "$dest/hengdesk-postgres.spdx.json" || note "annotate sbom"
  else
    note "sbom postgres"
  fi
fi
trivy version -f json > "$dest/trivy-version.json" || note "trivy version"
python3 "$ROOT/.github/scripts/trivy-image-gate.py" --write-index "$dest" "$@" || note "image index"
python3 "$ROOT/.github/scripts/trivy-image-gate.py" --strict-aggregate "$dest"
exit $?
