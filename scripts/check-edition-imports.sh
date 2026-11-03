#!/usr/bin/env bash
# 社区版不得依赖专业版（D-30）：检查源码（含所有 build tag 文件）、go.mod 与完整依赖图。
set -euo pipefail
cd "$(dirname "$0")/.."
PRO='github.com/SSStq1314/hengdesk-pro'
fail=0
# 1) 源码文本检查：覆盖任何 build tag 下的文件，以及前端
if grep -rnF --include='*.go' "\"$PRO" server \
  || grep -rnF --include='go.mod' --include='go.work' "$PRO" server \
  || grep -rnE --include='*.ts' --include='*.tsx' "from ['\"][^'\"]*hengdesk-pro|import\(['\"][^'\"]*hengdesk-pro" web/src; then
  echo "错误：社区版代码引用了专业版路径 $PRO" >&2; fail=1
fi
# 2) 社区版仓库内不得出现专业版目录
for d in server/pro server/internal/pro server/pkg/pro; do
  [ -e "$d" ] && { echo "错误：社区版仓库不应包含 $d（专业版放独立闭源 module）" >&2; fail=1; }
done
# 3) 依赖图检查（默认 tag 与所有平台）
if (cd server && go list -deps -test ./... | grep -F "$PRO"); then
  echo "错误：社区版依赖图包含专业版包" >&2; fail=1
fi
[ "$fail" = 0 ] && echo "OK：社区版未依赖专业版"
exit "$fail"
