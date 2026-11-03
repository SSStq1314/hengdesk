#!/usr/bin/env python3
"""govulncheck 门禁：读取 `govulncheck -format json` 输出（stdin）。
代码可达（finding 的调用栈首帧带 function，即 Symbol 级）→ 退出码 1，阻断合并；
不可达（Package/Module 级）→ GitHub warning，并与豁免文件比对。"""
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
        reachable.setdefault(osv, []).append(stack)
    else:
        lvl = "Package" if trace and trace[0].get("package") else "Module"
        unreachable.setdefault(osv, (lvl, trace[0].get("module", "") if trace else ""))

for osv, (lvl, mod) in sorted(unreachable.items()):
    if osv in reachable:
        continue
    note = "已登记豁免" if osv in exempt else "未登记豁免，请在 server/.govulncheck-exemptions.md 补充理由"
    print(f"::warning title=govulncheck 不可达漏洞 {osv}::{lvl} 级，{mod}：{summary.get(osv, '')}（{note}）")
for osv in sorted(exempt - set(unreachable) - set(reachable)):
    print(f"::notice title=govulncheck 豁免可清理::{osv} 已不再出现，可从豁免文件删除")

if reachable:
    for osv, stacks in sorted(reachable.items()):
        print(f"::error title=govulncheck 代码可达漏洞 {osv}::{summary.get(osv, '')}")
        for s in stacks[:3]:
            print(f"    调用栈：{s}")
    print(f"代码可达漏洞 {len(reachable)} 个，阻断合并（可达漏洞不接受豁免）")
    sys.exit(1)
print(f"govulncheck 门禁通过：可达 0，不可达 {len(unreachable)}（只告警）")
