#!/usr/bin/env python3
"""正式离线包镜像的 Trivy JSON 门禁。

阻断条件：HIGH 或 CRITICAL，且 FixedVersion 非空。有修复版本就必须处理，不设豁免表。
没有修复版本的 HIGH/CRITICAL 只打印 UNFIXED，不写入豁免，也不延长任何复查期限。
报告无法解析，或缺少 Results：失败。空结果才是通过。
"""
import json
import subprocess
import sys
import tempfile
from pathlib import Path

BLOCK_SEVERITY = {"HIGH", "CRITICAL"}


def evaluate(doc):
    if not isinstance(doc, dict) or "Results" not in doc:
        return 1, ["报告缺少 Results，拒绝放行"]
    results = doc["Results"]
    if results is None:
        results = []
    if not isinstance(results, list):
        return 1, ["Results 不是数组，拒绝放行"]
    block, unfixed = [], []
    for result in results:
        if not isinstance(result, dict):
            return 1, ["Results 项不是对象，拒绝放行"]
        target = result.get("Target") or doc.get("ArtifactName") or "unknown"
        vulns = result.get("Vulnerabilities") or []
        if not isinstance(vulns, list):
            return 1, [f"{target} 的 Vulnerabilities 不是数组，拒绝放行"]
        for vuln in vulns:
            if not isinstance(vuln, dict):
                return 1, [f"{target} 的漏洞项不是对象，拒绝放行"]
            sev = str(vuln.get("Severity") or "")
            if sev not in BLOCK_SEVERITY:
                continue
            vid = str(vuln.get("VulnerabilityID") or "unknown")
            pkg = str(vuln.get("PkgName") or "unknown")
            installed = str(vuln.get("InstalledVersion") or "")
            fixed = str(vuln.get("FixedVersion") or "").strip()
            line = f"{target} {sev} {vid} pkg={pkg} installed={installed} fixed={fixed or '-'}"
            if fixed:
                block.append("BLOCK " + line)
            else:
                unfixed.append("UNFIXED " + line)
    if block:
        return 1, block + unfixed
    return 0, unfixed


def run_file(path):
    try:
        doc = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"无法读取 Trivy 报告 {path}: {exc}", file=sys.stderr)
        return 1
    code, lines = evaluate(doc)
    for line in lines:
        print(line)
    if code:
        print(f"Trivy 门禁阻断：{sum(1 for line in lines if line.startswith('BLOCK '))} 个已有修复版本的高危或严重漏洞")
    else:
        print(f"Trivy 门禁通过：无已有修复版本的高危或严重漏洞，未修复 {sum(1 for line in lines if line.startswith('UNFIXED '))} 个")
    return code


def self_test():
    script = str(Path(__file__).resolve())
    cases = {
        "block.json": {
            "Results": [
                {
                    "Target": "hd-fixture-image",
                    "Vulnerabilities": [
                        {
                            "VulnerabilityID": "CVE-2099-99991",
                            "PkgName": "hd-fixture-pkg",
                            "InstalledVersion": "0.0.1",
                            "FixedVersion": "9.9.9",
                            "Severity": "HIGH",
                        }
                    ],
                }
            ]
        },
        "clean.json": {"Results": [{"Target": "hd-fixture-image", "Vulnerabilities": []}]},
        "unfixed.json": {
            "Results": [
                {
                    "Target": "hd-fixture-image",
                    "Vulnerabilities": [
                        {
                            "VulnerabilityID": "CVE-2099-99992",
                            "PkgName": "hd-fixture-pkg",
                            "InstalledVersion": "0.0.1",
                            "FixedVersion": "",
                            "Severity": "CRITICAL",
                        }
                    ],
                }
            ]
        },
        "broken.json": {},
    }
    expect = {"block.json": 1, "clean.json": 0, "unfixed.json": 0, "broken.json": 1}
    with tempfile.TemporaryDirectory() as tmp:
        for name, doc in cases.items():
            path = Path(tmp) / name
            path.write_text(json.dumps(doc), encoding="utf-8")
            proc = subprocess.run([sys.executable, script, str(path)], capture_output=True, text=True)
            if proc.returncode != expect[name]:
                print(f"自测失败 {name}: 期望 {expect[name]} 实际 {proc.returncode}", file=sys.stderr)
                print(proc.stdout, file=sys.stderr)
                return 1
            if name == "block.json" and "CVE-2099-99991" not in proc.stdout:
                print("自测失败：阻断输出没有包含虚构漏洞编号", file=sys.stderr)
                return 1
            if name == "unfixed.json" and "UNFIXED" not in proc.stdout:
                print("自测失败：未修复漏洞没有被标明", file=sys.stderr)
                return 1
    print("Trivy 门禁自测通过：虚构高危会失败，干净报告和未修复报告按规则处理")
    return 0


def main(argv):
    if len(argv) == 2 and argv[1] == "--self-test":
        return self_test()
    if len(argv) != 2:
        print("用法：trivy-image-gate.py REPORT.json | --self-test", file=sys.stderr)
        return 2
    return run_file(argv[1])


if __name__ == "__main__":
    sys.exit(main(sys.argv))
