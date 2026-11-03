#!/usr/bin/env bash
# 前端文案集中在 web/src/locales/zh-CN.ts：其它源码（注释除外）不得写死中文。
set -euo pipefail
cd "$(dirname "$0")/../web/src"
hits=$(grep -rnP --include='*.ts' --include='*.tsx' '[\x{4e00}-\x{9fff}]' . \
  | grep -v '^./locales/' \
  | grep -vP '^[^:]+:\d+:\s*(//|/\*|\*|\{/\*)' \
  | grep -vP '/\*[^*]*[\x{4e00}-\x{9fff}][^*]*\*/' || true)
if [ -n "$hits" ]; then
  echo "错误：以下文案未集中到 web/src/locales/zh-CN.ts：" >&2; echo "$hits" >&2; exit 1
fi
echo "OK：前端文案已集中"
