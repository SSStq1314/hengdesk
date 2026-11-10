#!/usr/bin/env python3
"""govulncheck 门禁：读取 `govulncheck -format json` 输出（stdin）。

- 代码可达且上游已有修复版本（finding.fixed_version 非空）→ 退出码 1，阻断合并（不可豁免，必须升级）。
- 代码可达但上游尚无修复（fixed_version 为空）→ 须在豁免文件登记，只告警不阻断；复查期限到后仍未修应升级或替换依赖。
- 不可达（Package/Module 级）→ GitHub warning，并与豁免文件比对。
"""
import json, re, sys

exempt_file = sys.argv[1]
exempt = set(re.findall(r"^\|\s*(GO-\d{4}-\d+)\s*\|", open(exempt_file, encoding="utf-8").read(), re.M))

dec = json.JSONDecoder()
data, objs, i = sys.stdin.read(), [], 0
while i < len(data):
    while i < len(data) and data[i].isspace():
        i += 1
    if i >= len(data):
        break
    o, i = dec.raw_decode(data, i)
    objs.append(o)

summary = {}
for o in objs:
    if "osv" in o:
        summary[o["osv"]["id"]] = o["osv"].get("summary", "")

# osv -> {"stacks": [...], "fixed": str|None}
reachable, unreachable = {}, {}
for o in objs:
    f = o.get("finding")
    if not f:
        continue
    trace = f.get("trace") or []
    osv = f["osv"]
    if trace and trace[0].get("function"):
        stack = " <- ".join(
            (t.get("package", "") + "." + t.get("function", "")).strip(".") for t in trace)
        ent = reachable.setdefault(osv, {"stacks": [], "fixed": f.get("fixed_version")})
        ent["stacks"].append(stack)
        if f.get("fixed_version"):
            ent["fixed"] = f.get("fixed_version")
    else:
        lvl = "Package" if trace and trace[0].get("package") else "Module"
        unreachable.setdefault(osv, (lvl, trace[0].get("module", "") if trace else ""))

for osv, (lvl, mod) in sorted(unreachable.items()):
    if osv in reachable:
        continue
    note = "已登记豁免" if osv in exempt else "未登记豁免，请在 server/.govulncheck-exemptions.md 补充理由"
    print(f"::warning title=govulncheck 不可达漏洞 {osv}::{lvl} 级，{mod}：{summary.get(osv, '')}（{note}）")

must_fix, no_fix_unexempt = [], []
for osv, ent in sorted(reachable.items()):
    stacks = ent["stacks"]
    fixed = ent.get("fixed")
    if fixed:
        must_fix.append((osv, stacks, fixed))
        continue
    if osv in exempt:
        print(f"::warning title=govulncheck 可达但上游无修复 {osv}::{summary.get(osv, '')}（已登记豁免；复查期限见豁免表）")
        for s in stacks[:3]:
            print(f"    调用栈：{s}")
    else:
        no_fix_unexempt.append((osv, stacks))

for osv in sorted(exempt - set(unreachable) - set(reachable)):
    print(f"::notice title=govulncheck 豁免可清理::{osv} 已不再出现，可从豁免文件删除")

if must_fix or no_fix_unexempt:
    for osv, stacks, fixed in must_fix:
        print(f"::error title=govulncheck 代码可达漏洞 {osv}::{summary.get(osv, '')}（已有修复版本 {fixed}，必须升级）")
        for s in stacks[:3]:
            print(f"    调用栈：{s}")
    for osv, stacks in no_fix_unexempt:
        print(f"::error title=govulncheck 代码可达且上游无修复 {osv}::{summary.get(osv, '')}（请在 server/.govulncheck-exemptions.md 登记理由与复查期限）")
        for s in stacks[:3]:
            print(f"    调用栈：{s}")
    n = len(must_fix) + len(no_fix_unexempt)
    print(f"代码可达漏洞 {n} 个未处理，阻断合并")
    sys.exit(1)

print(f"govulncheck 门禁通过：可达已修复 0，可达无修复已豁免 {sum(1 for e in reachable.values() if not e.get('fixed'))}，不可达 {len(unreachable)}（只告警）")
