#!/usr/bin/env bash
# 秘密扫描门禁。
# 1. 用运行时拼出的虚构值证明：命中必须失败，且日志和报告不出现原文。
# 2. 用无秘密目录证明：干净输入必须通过。
# 3. 扫描仓库。失败即阻断，没有 continue-on-error。
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
bin="${GITLEAKS_BIN:-gitleaks}"
report="${GITLEAKS_REPORT:-$(mktemp --suffix=.json)}"

if ! command -v "$bin" >/dev/null 2>&1 && [ ! -x "$bin" ]; then
  echo "找不到 gitleaks：$bin" >&2
  exit 1
fi

# 虚构值拆开存放，避免门禁脚本自己被扫成秘密。
# 形状像访问密钥，字符是 HDFIXTURE / NOTREAL，不是已签发的密钥，也不使用以 EXAMPLE 结尾的文档样本（默认规则会放行那种样本）。
fixture_token="$(printf '%s%s%s' 'AKIA' 'HDFIXTURE' 'NOTREAL')"
fixture=$(mktemp -d)
printf 'aws_access_key_id=%s\n' "$fixture_token" > "$fixture/not-a-real-credential.txt"
set +e
fixture_out=$("$bin" detect --no-git --source "$fixture" --redact --no-banner 2>&1)
fixture_rc=$?
set -e
rm -rf "$fixture"
if [ "$fixture_rc" -eq 0 ]; then
  echo "门禁自测失败：虚构秘密没有让 gitleaks 失败" >&2
  exit 1
fi
if printf '%s' "$fixture_out" | grep -F "$fixture_token" >/dev/null; then
  echo "门禁自测失败：日志包含虚构秘密原文" >&2
  exit 1
fi
echo "gitleaks 虚构样本按预期失败，日志已脱敏"

clean=$(mktemp -d)
printf 'hello\n' > "$clean/readme.txt"
"$bin" detect --no-git --source "$clean" --redact --no-banner >/dev/null
rm -rf "$clean"
echo "gitleaks 无秘密目录按预期通过"

rm -f "$report"
"$bin" detect --source "$root" --config "$root/.gitleaks.toml" --redact --no-banner --report-path "$report"
if [ -f "$report" ] && grep -F "$fixture_token" "$report" >/dev/null; then
  echo "仓库报告包含虚构秘密原文" >&2
  exit 1
fi
echo "gitleaks 仓库扫描通过"
