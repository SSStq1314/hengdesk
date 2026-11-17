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

# 虚构样本是无法解析的私钥文本，不是可用的真实密钥。片段拆开存放，避免脚本自己被扫中。
pem_begin="$(printf '%s%s' '-----BEGIN HD FIXTURE ' 'PRIVATE KEY-----')"
pem_body="$(printf '%s' 'HD-FIXTURE-NOT-A-REAL-PRIVATE-KEY-VALUE-NOT-A-REAL-PRIVATE-KEY-VALUE')"
pem_end="$(printf '%s%s' '-----END HD FIXTURE ' 'PRIVATE KEY-----')"
write_fake_pem() {
  printf '%s\n%s\n%s\n' "$pem_begin" "$pem_body" "$pem_end"
}
fixture=$(mktemp -d)
write_fake_pem > "$fixture/not-a-real-credential.txt"
set +e
fixture_out=$("$bin" detect --no-git --source "$fixture" --redact --no-banner 2>&1)
fixture_rc=$?
set -e
rm -rf "$fixture"
if [ "$fixture_rc" -eq 0 ]; then
  echo "门禁自测失败：虚构秘密没有让 gitleaks 失败" >&2
  exit 1
fi
if printf '%s' "$fixture_out" | grep -F "$pem_body" >/dev/null; then
  echo "门禁自测失败：日志包含虚构秘密原文" >&2
  exit 1
fi
echo "gitleaks 虚构样本按预期失败，日志已脱敏"

# 同一路径上再加一条泄漏。精确指纹不能把整个文件放行。
same=$(mktemp -d)
mkdir -p "$same/server/internal/gateway"
cp "$root/server/internal/gateway/gateway_test.go" "$same/server/internal/gateway/gateway_test.go"
{
  printf '%s\n' '// added fictional leak'
  write_fake_pem
} >> "$same/server/internal/gateway/gateway_test.go"
git -C "$same" init -q
git -C "$same" config user.email hd-fixture@example.invalid
git -C "$same" config user.name hd-fixture
git -C "$same" add server/internal/gateway/gateway_test.go
git -C "$same" commit -q -m 'fixture'
same_report="$same/report.json"
set +e
same_out=$("$bin" detect --source "$same" --config "$root/.gitleaks.toml" --gitleaks-ignore-path "$root/.gitleaksignore" --redact --no-banner --report-format json --report-path "$same_report" 2>&1)
same_rc=$?
set -e
if [ "$same_rc" -eq 0 ]; then
  echo "门禁自测失败：同一文件新增的虚构泄漏没有被挡住" >&2
  exit 1
fi
if printf '%s' "$same_out" | grep -F "$pem_body" >/dev/null; then
  echo "门禁自测失败：新增泄漏的日志包含原文" >&2
  exit 1
fi
python3 - "$same_report" << 'PY'
import json, sys
data = json.load(open(sys.argv[1]))
rules = {item.get("RuleID") for item in data}
files = {item.get("File") for item in data}
if "private-key" not in rules:
    raise SystemExit("新增泄漏没有以 private-key 被报告")
if not any(path.endswith("gateway_test.go") for path in files):
    raise SystemExit("新增泄漏没有落在原测试文件路径上")
if any(item.get("Secret") not in (None, "REDACTED") for item in data):
    raise SystemExit("报告没有脱敏")
PY
rm -rf "$same"
echo "gitleaks 同一文件新增虚构泄漏按预期失败"

clean=$(mktemp -d)
printf 'hello\n' > "$clean/readme.txt"
"$bin" detect --no-git --source "$clean" --redact --no-banner >/dev/null
rm -rf "$clean"
echo "gitleaks 无秘密目录按预期通过"

rm -f "$report"
"$bin" detect --source "$root" --config "$root/.gitleaks.toml" --gitleaks-ignore-path "$root/.gitleaksignore" --redact --no-banner --report-format json --report-path "$report"
if [ -f "$report" ] && grep -F "$pem_body" "$report" >/dev/null; then
  echo "仓库报告包含虚构秘密原文" >&2
  exit 1
fi
echo "gitleaks 仓库扫描通过"
