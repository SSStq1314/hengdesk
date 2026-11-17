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


def archive_unfixed(path, lines):
    unfixed = [line for line in lines if line.startswith("UNFIXED ")]
    dest = Path(str(path) + ".unfixed.txt")
    if unfixed:
        text = (
            "这些是报告里没有修复版本的 HIGH/CRITICAL。这不是无高危，也不是豁免。\n"
            + "\n".join(unfixed)
            + "\n"
        )
    else:
        text = "本报告没有未修复的 HIGH/CRITICAL。这不表示镜像没有其他级别的漏洞。\n"
    dest.write_text(text, encoding="utf-8")


def run_file(path):
    try:
        raw = Path(path).read_text(encoding="utf-8")
        doc = json.loads(raw)
    except (OSError, json.JSONDecodeError) as exc:
        print(f"无法读取 Trivy 报告 {path}: {exc}", file=sys.stderr)
        return 1
    code, lines = evaluate(doc)
    archive_unfixed(path, lines)
    for line in lines:
        print(line)
    unfixed_n = sum(1 for line in lines if line.startswith("UNFIXED "))
    if code:
        print(f"Trivy 门禁阻断：{sum(1 for line in lines if line.startswith('BLOCK '))} 个已有修复版本的高危或严重漏洞")
        if unfixed_n:
            print(f"同时完整记录 {unfixed_n} 个没有修复版本的 HIGH/CRITICAL。这不是无高危，也不是豁免。")
    elif unfixed_n:
        print(f"Trivy 门禁未因已有修复版本而阻断。另有 {unfixed_n} 个没有修复版本的 HIGH/CRITICAL，已完整写入 UNFIXED。这不是无高危，也不是豁免。")
    else:
        print("Trivy 门禁通过：报告里没有 HIGH/CRITICAL。这不表示镜像没有其他级别的漏洞。")
    return code


def write_index(directory, images):
    """把每张镜像的 id、repo digest，以及 Trivy 与漏洞库版本写入 images.json。"""
    root = Path(directory)
    meta = []
    for image in images:
        proc = subprocess.run(["docker", "image", "inspect", image], capture_output=True, text=True)
        item = {"image": image, "present": proc.returncode == 0}
        if proc.returncode == 0:
            try:
                info = json.loads(proc.stdout)[0]
            except json.JSONDecodeError:
                item["inspect_error"] = True
            else:
                item["id"] = info.get("Id")
                item["repo_digests"] = info.get("RepoDigests") or []
        meta.append(item)
    version = {}
    version_path = root / "trivy-version.json"
    if version_path.is_file():
        try:
            version = json.loads(version_path.read_text(encoding="utf-8"))
        except json.JSONDecodeError:
            version = {"parse_error": True}
    (root / "images.json").write_text(
        json.dumps({"images": meta, "trivy": version}, indent=2) + "\n",
        encoding="utf-8",
    )
    print(f"已写入 {root / 'images.json'}，镜像 {len(meta)} 张")
    return 0


def aggregate(directory, strict=False):
    """读取目录里每张镜像的报告。任一阻断、解析失败或扫描错误都记下来，最后统一返回失败。

    strict=True 时，没有修复版本的 HIGH/CRITICAL 也失败。这不是豁免，也不能标成通过。
    """
    root = Path(directory)
    if not root.is_dir():
        print(f"报告目录不存在：{directory}", file=sys.stderr)
        return 1
    reports = sorted(p for p in root.glob("*.json") if p.name != "trivy-version.json" and p.name != "images.json")
    errors = root / "errors.txt"
    lines = []
    failed = False
    if not reports and not errors.exists():
        print("没有镜像报告，拒绝放行", file=sys.stderr)
        return 1
    for path in reports:
        try:
            doc = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            failed = True
            lines.append(f"SCAN_ERROR {path.name} 报告无法解析: {exc}")
            continue
        code, found = evaluate(doc)
        archive_unfixed(path, found)
        lines.extend(found)
        if code:
            failed = True
    if errors.exists():
        err_text = errors.read_text(encoding="utf-8").strip()
        if err_text:
            failed = True
            lines.extend(err_text.splitlines())
    summary = root / "summary.txt"
    block_n = sum(1 for line in lines if line.startswith("BLOCK "))
    unfixed_n = sum(1 for line in lines if line.startswith("UNFIXED "))
    error_n = sum(1 for line in lines if line.startswith("SCAN_ERROR"))
    header = [
        f"BLOCK {block_n}",
        f"UNFIXED {unfixed_n}",
        f"SCAN_ERROR {error_n}",
        "UNFIXED 是没有修复版本的 HIGH/CRITICAL，不是无高危，也不是豁免。",
    ]
    body = "\n".join(header + lines) + "\n"
    summary.write_text(body, encoding="utf-8")
    print(body, end="")
    if strict and unfixed_n:
        failed = True
        print(f"严格模式：{unfixed_n} 个没有修复版本的 HIGH/CRITICAL 不能标为通过，也没有风险接受。")
    if failed or block_n or error_n:
        print("Trivy 聚合结果：失败。已收集全部镜像的报告后再返回。")
        return 1
    if unfixed_n:
        print(f"Trivy 聚合结果：没有已有修复版本的阻断项。另有 {unfixed_n} 个 UNFIXED，不能标为最终包安全通过。")
        return 0
    print("Trivy 聚合结果：没有 HIGH/CRITICAL，也没有扫描错误。")
    return 0


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
        "invalid.json": "this-is-not-json",
        "empty.json": "",
    }
    expect = {
        "block.json": 1,
        "clean.json": 0,
        "unfixed.json": 0,
        "broken.json": 1,
        "invalid.json": 1,
        "empty.json": 1,
    }
    with tempfile.TemporaryDirectory() as tmp:
        for name, doc in cases.items():
            path = Path(tmp) / name
            if isinstance(doc, str):
                path.write_text(doc, encoding="utf-8")
            else:
                path.write_text(json.dumps(doc), encoding="utf-8")
            proc = subprocess.run([sys.executable, script, str(path)], capture_output=True, text=True)
            if proc.returncode != expect[name]:
                print(f"自测失败 {name}: 期望 {expect[name]} 实际 {proc.returncode}", file=sys.stderr)
                print(proc.stdout, file=sys.stderr)
                print(proc.stderr, file=sys.stderr)
                return 1
            if name == "block.json" and "CVE-2099-99991" not in proc.stdout:
                print("自测失败：阻断输出没有包含虚构漏洞编号", file=sys.stderr)
                return 1
            if name == "unfixed.json":
                if "UNFIXED" not in proc.stdout or "不是无高危" not in proc.stdout:
                    print("自测失败：未修复漏洞没有被完整标明为仍是高危", file=sys.stderr)
                    return 1
                archived = Path(str(path) + ".unfixed.txt").read_text(encoding="utf-8")
                if "CVE-2099-99992" not in archived or "不是无高危" not in archived:
                    print("自测失败：未修复漏洞没有归档", file=sys.stderr)
                    return 1
        batch = Path(tmp) / "batch"
        batch.mkdir()
        (batch / "a-block.json").write_text((Path(tmp) / "block.json").read_text(encoding="utf-8"), encoding="utf-8")
        (batch / "b-clean.json").write_text((Path(tmp) / "clean.json").read_text(encoding="utf-8"), encoding="utf-8")
        (batch / "c-unfixed.json").write_text((Path(tmp) / "unfixed.json").read_text(encoding="utf-8"), encoding="utf-8")
        (batch / "d-invalid.json").write_text("this-is-not-json", encoding="utf-8")
        (batch / "errors.txt").write_text("SCAN_ERROR hd-fixture-missing 镜像不存在\n", encoding="utf-8")
        proc = subprocess.run([sys.executable, script, "--aggregate", str(batch)], capture_output=True, text=True)
        if proc.returncode == 0:
            print("自测失败：聚合了阻断和扫描错误却返回成功", file=sys.stderr)
            return 1
        for needle in ("CVE-2099-99991", "CVE-2099-99992", "SCAN_ERROR", "hd-fixture-missing", "不是无高危"):
            if needle not in proc.stdout and needle not in (batch / "summary.txt").read_text(encoding="utf-8"):
                print(f"自测失败：聚合结果缺少 {needle}", file=sys.stderr)
                return 1
        if "CVE-2099-99992" not in proc.stdout:
            print("自测失败：第一张阻断后没有继续收集 UNFIXED", file=sys.stderr)
            return 1
        only = Path(tmp) / "unfixed-only"
        only.mkdir()
        (only / "c-unfixed.json").write_text((Path(tmp) / "unfixed.json").read_text(encoding="utf-8"), encoding="utf-8")
        loose = subprocess.run([sys.executable, script, "--aggregate", str(only)], capture_output=True, text=True)
        tight = subprocess.run([sys.executable, script, "--strict-aggregate", str(only)], capture_output=True, text=True)
        if loose.returncode != 0 or tight.returncode == 0 or "不能标为通过" not in tight.stdout:
            print("自测失败：严格模式没有阻断无修复高危", file=sys.stderr)
            print(loose.returncode, tight.returncode, tight.stdout, file=sys.stderr)
            return 1
    print("Trivy 门禁自测通过：单报告失败关闭，聚合时阻断后仍收集其余报告，严格模式不放行 UNFIXED")
    return 0


def main(argv):
    if len(argv) == 2 and argv[1] == "--self-test":
        return self_test()
    if len(argv) == 3 and argv[1] in ("--aggregate", "--strict-aggregate"):
        return aggregate(argv[2], strict=argv[1] == "--strict-aggregate")
    if len(argv) >= 3 and argv[1] == "--write-index":
        return write_index(argv[2], argv[3:])
    if len(argv) != 2:
        print("用法：trivy-image-gate.py REPORT.json | --aggregate DIR | --write-index DIR IMAGE... | --self-test", file=sys.stderr)
        return 2
    return run_file(argv[1])


if __name__ == "__main__":
    sys.exit(main(sys.argv))
