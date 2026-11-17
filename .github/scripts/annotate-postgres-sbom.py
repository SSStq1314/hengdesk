#!/usr/bin/env python3
"""给 Trivy 生成的 hengdesk/postgres SPDX 补上它扫不到的替换件。

Trivy 能看到 gosu 这个二进制，但许可证是 NOASSERTION，也看不到我们拷进去的 vector.so。
这两条的许可证来自镜像里的许可证文件，不是 Trivy 从包数据库推断的。
"""
import json
import sys
from pathlib import Path

GOSU_URL = "https://github.com/tianon/gosu/archive/refs/tags/1.20.tar.gz"
PGVECTOR_URL = "https://github.com/pgvector/pgvector/archive/refs/tags/v0.8.7.tar.gz"
GOSU_NOTE = "许可证取自镜像内 /usr/local/share/hengdesk/licenses/gosu-LICENSE。不是 Trivy 从包库推断的。"
PGVECTOR_NOTE = (
    "pgvector 0.8.7 在同一套 PG17 头文件上编译后装入。"
    "许可证文本在 /usr/local/share/hengdesk/licenses/pgvector-LICENSE。"
    "Trivy 扫不到这个扩展，这一条是构建记录补上的。"
)
PGVECTOR_NAME = "usr/local/lib/postgresql/vector.so"
PGVECTOR_ID = "SPDXRef-Package-hengdesk-pgvector"


def annotate(doc):
    name = str(doc.get("name") or "")
    if not name.startswith("hengdesk/postgres"):
        raise ValueError(f"SBOM 镜像名不是 hengdesk/postgres：{name}")
    packages = doc.get("packages")
    if not isinstance(packages, list):
        raise ValueError("SBOM 没有 packages 数组")
    image = next((pkg for pkg in packages if str(pkg.get("name") or "").startswith("hengdesk/postgres")), None)
    if image is None or not image.get("SPDXID"):
        raise ValueError("SBOM 没有容器包")
    gosu = [pkg for pkg in packages if pkg.get("name") in {"usr/local/bin/gosu", "github.com/tianon/gosu"}]
    if not any(pkg.get("name") == "usr/local/bin/gosu" for pkg in gosu):
        raise ValueError("SBOM 没有替换后的 /usr/local/bin/gosu")
    for pkg in gosu:
        pkg["licenseConcluded"] = "Apache-2.0"
        pkg["licenseDeclared"] = "Apache-2.0"
        pkg["downloadLocation"] = GOSU_URL
        pkg["comment"] = GOSU_NOTE
    if not any(pkg.get("name") == PGVECTOR_NAME for pkg in packages):
        packages.append(
            {
                "SPDXID": PGVECTOR_ID,
                "name": PGVECTOR_NAME,
                "versionInfo": "0.8.7",
                "downloadLocation": PGVECTOR_URL,
                "filesAnalyzed": False,
                "licenseConcluded": "PostgreSQL",
                "licenseDeclared": "PostgreSQL",
                "copyrightText": "NOASSERTION",
                "primaryPackagePurpose": "LIBRARY",
                "comment": PGVECTOR_NOTE,
            }
        )
    relationships = doc.setdefault("relationships", [])
    if not isinstance(relationships, list):
        raise ValueError("SBOM 的 relationships 不是数组")
    already = any(
        rel.get("relatedSpdxElement") == PGVECTOR_ID and rel.get("relationshipType") == "CONTAINS"
        for rel in relationships
        if isinstance(rel, dict)
    )
    if not already:
        relationships.append(
            {
                "spdxElementId": image["SPDXID"],
                "relatedSpdxElement": PGVECTOR_ID,
                "relationshipType": "CONTAINS",
            }
        )
    return doc


def self_test():
    base = {
        "name": "hengdesk/postgres:candidate",
        "packages": [
            {"SPDXID": "SPDXRef-ContainerImage-test", "name": "hengdesk/postgres:candidate"},
            {
                "SPDXID": "SPDXRef-Application-gosu",
                "name": "usr/local/bin/gosu",
                "licenseConcluded": "NOASSERTION",
                "licenseDeclared": "NOASSERTION",
                "downloadLocation": "NONE",
            },
        ],
        "relationships": [],
    }
    doc = annotate(json.loads(json.dumps(base)))
    annotate(doc)
    gosu = next(pkg for pkg in doc["packages"] if pkg["name"] == "usr/local/bin/gosu")
    vector = next(pkg for pkg in doc["packages"] if pkg["name"] == PGVECTOR_NAME)
    contains = [rel for rel in doc["relationships"] if rel.get("relatedSpdxElement") == PGVECTOR_ID]
    if gosu["licenseConcluded"] != "Apache-2.0" or vector["licenseConcluded"] != "PostgreSQL":
        print("自测失败：许可证没有写上", file=sys.stderr)
        return 1
    if vector["versionInfo"] != "0.8.7" or len(contains) != 1:
        print("自测失败：pgvector 条目或关系不对", file=sys.stderr)
        return 1
    foreign = json.loads(json.dumps(base))
    foreign["name"] = "pgvector/pgvector:0.8.7-pg17"
    try:
        annotate(foreign)
    except ValueError:
        pass
    else:
        print("自测失败：别人的镜像名也被接受了", file=sys.stderr)
        return 1
    print("postgres SBOM 补注自测通过")
    return 0


def main(argv):
    if len(argv) == 2 and argv[1] == "--self-test":
        return self_test()
    if len(argv) != 2:
        print("用法：annotate-postgres-sbom.py SPDX.json | --self-test", file=sys.stderr)
        return 2
    path = Path(argv[1])
    try:
        doc = json.loads(path.read_text(encoding="utf-8"))
        annotate(doc)
    except (OSError, json.JSONDecodeError, ValueError) as exc:
        print(f"无法补注 SBOM {path}: {exc}", file=sys.stderr)
        return 1
    path.write_text(json.dumps(doc, indent=2) + "\n", encoding="utf-8")
    print(f"已补注 {path}：gosu 为 Apache-2.0，vector.so 0.8.7 为 PostgreSQL")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
