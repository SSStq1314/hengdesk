# 候选镜像（未发布）

这是候选构建记录，不是安全通过，也不是发布授权。`deploy/docker-compose.yml` 和 `deploy/pack-offline.sh` 仍使用原来的官方标签。#82 不因本文件合并。

五张镜像的 HIGH/CRITICAL（包括没有修复版本的）在同一次 Trivy 扫描里都是 0 之前，不跑备份恢复、旧包升级、失败回退、S3 兼容或 D-50。没有无修复高危的风险接受，也不设全面忽略。

## 当前钉住的上游

| 用途 | 现用标签 | 候选 | 状态 |
|---|---|---|---|
| PostgreSQL 17 + pgvector 0.8.7 | `pgvector/pgvector:0.8.7-pg17` | 自有镜像 `hengdesk/postgres` | 官方 bookworm / trixie / `postgres:17-alpine` 都不能让 HIGH/CRITICAL 为 0。阻塞来自 `usr/local/bin/gosu` 的 Go 1.24.6，bookworm 另有一批 Debian 无修复高危 |
| SeaweedFS | `chrislusf/seaweedfs:4.52` | 官方 `4.54` | 候选扫描 HIGH/CRITICAL 为 0。许可证仍是 Apache-2.0。S3、升级和 D-50 还没跑 |
| Web | `nginx:1.28-alpine` 加构建 | 同一 Dockerfile 的 `apk upgrade` | 必须等这次构建扫描的 digest 和包清单，不能先写成已修 |
| Valkey | `valkey/valkey:8.1.11-alpine` | 不变 | 候选扫描没有 HIGH/CRITICAL |
| server | 本仓库 `server/Dockerfile` | 同一 Dockerfile，含 go-jose v4.1.9 | 上一轮 CI 产物没有 HIGH/CRITICAL。以本次候选作业的 digest 为准 |

## 自有 Postgres 镜像

不重编 PostgreSQL 服务器。基础镜像是官方 `postgres:17-alpine` 的 linux/amd64 manifest：

`postgres:17-alpine@sha256:005c5c37db96c1e6652a79ea31f9886402fba81ac95dc2db897739121dd7f8f2`

对应多架构索引是 `sha256:a77aa70e641402a21411b92b721c58d10f23b50ae5fdd2333efd22d166e1e5f7`，配置 id 是 `sha256:6b75136145ddabf3d9a4b69660402082de7c56b734cfe50555b0674774dd742a`。Trivy 0.76.2、漏洞库版本 2（更新于 2026-11-17 14:59 Asia/Shanghai）对这个基础镜像的结果是：HIGH/CRITICAL 只有 `usr/local/bin/gosu` 里的 Go stdlib v1.24.6，UNFIXED 为 0。空的 `alpine:3.24.4`（`sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf`）则是 0。

替换两样东西：

- gosu `1.20`，源码包 SHA256 `207a2045e890d3ca3326027c5f585733085960b401a49dbc00e3bb628c054cf8`，用 `golang:1.26.11-alpine@sha256:f496f8c537ed506626ee1a6913afb32c45068a48697163c088f604cc775a0c38` 编译。不剥离 buildinfo。
- pgvector `v0.8.7`，源码包 SHA256 `cac0b10c360f05b2d521200105ba3697e773d4cd3731f5a915a7e37ebe0bea85`，在同一 PG17 头文件上 `make OPTFLAGS=""`。许可证是 PostgreSQL 许可证。gosu 许可证是 Apache-2.0。两份许可证打进 `/usr/local/share/hengdesk/licenses/`。

镜像名是 `hengdesk/postgres`，不是 `pgvector/pgvector`。

构建和入口验证：

```bash
docker build --provenance=false --sbom=false -t hengdesk/postgres:candidate deploy/images/postgres-pgvector
bash deploy/images/postgres-pgvector/verify.sh hengdesk/postgres:candidate
```

`verify.sh` 检查 gosu 降权后的 uid、数据目录属主、由 postgres 用户创建的文件、`CREATE EXTENSION vector` 的版本 `0.8.7`，以及 SIGTERM 后日志里的关闭记录和退出码 0。

## 可复现重建

候选作业是 `.github/workflows/candidate-images.yml`，只在手动触发时跑。它构建 `hengdesk/postgres`、`hengdesk/server`、`hengdesk/web`，拉取 Valkey `8.1.11-alpine` 和 SeaweedFS `4.54`，用严格聚合扫描。artifact 名是 `candidate-image-evidence`，里面有每张报告、`images.json`、`trivy-version.json`、gosu 的 `go version -m`、web 的 `apk info -v`，以及 postgres 镜像的 SPDX SBOM。

```bash
gh workflow run candidate-images.yml --ref <分支>
```

## 回退

默认 compose 和离线包清单还没改。回退就是继续使用 `pgvector/pgvector:0.8.7-pg17` 和 `chrislusf/seaweedfs:4.52`。不要把候选标签写进已发布的离线包。

## 后续补丁

1. 更换基础 digest、gosu 标签或 pgvector 标签时，同时更换校验和，并重新跑本作业。
2. 五张候选的 HIGH/CRITICAL 都是 0 之后，才把标签写入 compose 和 `pack-offline.sh`，然后跑备份恢复、旧包升级、失败回退、S3 和 D-50。
3. 扫描没有全部为 0 时，保持 #82 不合并，按 CVE、路径和修复版本报告，不新增豁免。
