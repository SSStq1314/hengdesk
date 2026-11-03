#!/usr/bin/env bash
# 前端文案集中在 web/src/locales/zh-CN.ts：其它源码去掉注释后不得写死中文。
# 同时检查 index.html 之外的可见文案；行内注释会先被剥掉，避免「代码 + 中文注释」同行时漏检。
set -euo pipefail
cd "$(dirname "$0")/../web/src"
hits=$(grep -rnP --include='*.ts' --include='*.tsx' '[\x{4e00}-\x{9fff}]' . \
  | grep -v '^./locales/' \
  | perl -CSD -ne 'my ($loc,$code)=/^([^:]+:\d+:)(.*)$/ or next; $code=~s{/\*.*?\*/}{}g; $code=~s{\{/\*.*$}{}; $code=~s{^\s*(\*|/\*).*$}{}; $code=~s{(^|\s)//.*$}{}; print "$loc$code\n" if $code=~/[\x{4e00}-\x{9fff}]/' \
  || true)
if [ -n "$hits" ]; then
  echo "错误：以下文案未集中到 web/src/locales/zh-CN.ts：" >&2; echo "$hits" >&2; exit 1
fi
echo "OK：前端文案已集中"
