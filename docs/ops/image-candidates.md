# 候选镜像（未发布）

这是候选构建记录，不是安全通过，也不是发布授权。`deploy/docker-compose.yml` 和 `deploy/pack-offline.sh` 仍使用原来的官方标签。#82 不因本文件合并。

五张镜像的 HIGH/CRITICAL（包括没有修复版本的）在同一次 Trivy 扫描里都是 0 之前，不跑备份恢复、旧包升级、失败回退、S3 兼容或 D-50。没有无修复高危的风险接受，也不设全面忽略。

## 当前钉住的上游

| 用途 | 现用标签 | 候选 | 状态 |
|---|---|---|---|
| PostgreSQL 17 + pgvector 0.8.7 | `pgvector/pgvector:0.8.7-pg17` | 自有镜像 `hengdesk/postgres` | 官方 bookworm / trixie / `postgres:17-alpine` 都不能让 HIGH/CRITICAL 为 0。阻塞来自 `usr/local/bin/gosu` 的 Go 1.24.6，bookworm 另有一批 Debian 无修复高危 |
| SeaweedFS | `chrislusf/seaweedfs:4.52` | 官方 `4.54` | 候选扫描 HIGH/CRITICAL 为 0。许可证仍是 Apache-2.0。S3、升级和 D-50 还没跑 |
| Web | `nginx:1.28-alpine` 加构建 | 卸掉 nginx.org 钉死的 `nginx=1.28.3-r1`，改用 Alpine 3.23 的 `nginx>=1.28.3-r6` | 2026-11-17 19:32（Asia/Shanghai）那次产物仍是 r1。`apk upgrade` 升了 curl、libxml2，但没动 world 里的 nginx.org 钉。还没重扫 |
| Valkey | `valkey/valkey:8.1.11-alpine` | 不变 | 候选扫描没有 HIGH/CRITICAL |
| server | 本仓库 `server/Dockerfile` | 同一 Dockerfile，含 go-jose v4.1.9 | 上一轮 CI 产物没有 HIGH/CRITICAL。以本次候选作业的 digest 为准 |

## 自有 Postgres 镜像

不重编 PostgreSQL 服务器。基础镜像是官方 `postgres:17-alpine` 的 linux/amd64 manifest：

`postgres:17-alpine@sha256:005c5c37db96c1e6652a79ea31f9886402fba81ac95dc2db897739121dd7f8f2`

对应多架构索引是 `sha256:a77aa70e641402a21411b92b721c58d10f23b50ae5fdd2333efd22d166e1e5f7`，配置 id 是 `sha256:6b75136145ddabf3d9a4b69660402082de7c56b734cfe50555b0674774dd742a`。Trivy 0.76.2、漏洞库版本 2（更新于 2026-11-17 14:59 Asia/Shanghai）对这个基础镜像的结果是：HIGH/CRITICAL 只有 `usr/local/bin/gosu` 里的 Go stdlib v1.24.6，UNFIXED 为 0。空的 `alpine:3.24.4`（`sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf`）则是 0。

替换两样东西：

- gosu `1.20`，源码包 SHA256 `207a2045e890d3ca3326027c5f585733085960b401a49dbc00e3bb628c054cf8`，用 `golang:1.26.11-alpine@sha256:f496f8c537ed506626ee1a6913afb32c45068a48697163c088f604cc775a0c38` 编译。不剥离 buildinfo。
- pgvector `v0.8.7`，源码包 SHA256 `cac0b10c360f05b2d521200105ba3697e773d4cd3731f5a915a7e37ebe0bea85`，在同一 PG17 头文件上 `make OPTFLAGS=""`。构建包钉为 `build-base=0.5-r4`、`clang21=21.1.8-r3`、`llvm21=21.1.8-r1`。pgxs 要 `/usr/lib/llvm21/bin/llvm-lto`，只装 clang21 不够。装好的 `.so`、bitcode 和这份包清单进镜像。许可证是 PostgreSQL 许可证。gosu 许可证是 Apache-2.0。两份许可证打进 `/usr/local/share/hengdesk/licenses/`。

镜像名是 `hengdesk/postgres`，不是 `pgvector/pgvector`。

构建和入口验证：

```bash
docker build --provenance=false --sbom=false -t hengdesk/postgres:candidate deploy/images/postgres-pgvector
bash deploy/images/postgres-pgvector/verify.sh hengdesk/postgres:candidate
```

`verify.sh` 检查 gosu 降权后的 uid、数据目录属主、由 postgres 用户创建的文件、`CREATE EXTENSION vector` 的版本 `0.8.7`，以及 SIGTERM 后日志里的关闭记录和退出码 0。

## 可复现重建

候选作业是 `.github/workflows/candidate-images.yml`。这个分支的 pull request 和手动触发都会跑。它构建 `hengdesk/postgres`、`hengdesk/server`、`hengdesk/web`，拉取 Valkey `8.1.11-alpine` 和 SeaweedFS `4.54`，用严格聚合扫描。artifact 名是 `candidate-image-evidence`，里面有每张报告、`images.json`、`trivy-version.json`、gosu 的 `go version -m`、pgvector 构建包清单、web 的 `apk info -v` 和 `apk policy nginx`，以及 postgres 镜像的 SPDX SBOM。

2026-11-17 19:32（Asia/Shanghai）的作业 `44074473122`（提交 `ae8eb99`，Trivy 0.76.2，漏洞库版本 2，库更新于 2026-11-17 14:59 Asia/Shanghai）没有通过。Postgres 镜像没建出来：`make install` 要 `/usr/lib/llvm21/bin/llvm-lto`，当时只装了 clang21。Web 产物 `sha256:0108b7ad3841959e76e54ff5c5d6053606cb5cd9f54ea59c4030abc8e9321c0e` 仍是 nginx `1.28.3-r1`，5 个 HIGH 都有修复版本（最高修到 `1.28.3-r6`）。同一次里下面三张没有 HIGH/CRITICAL：server `sha256:b55968c6273d0234e9bdfab264d2a2dd4835127fe7879406f1b23fffa3527bca`，Valkey `sha256:2602ace2be6569bc2bbb06f873776516ab9089821525be34f4305c67eb9db02a`，SeaweedFS 4.54 `sha256:4b42c212543a0acd4c0e675215ecdcaf62a186ef98def76c7e12701871222d15`。这三张为零不表示五张都过了，也不表示可以发布。

2026-11-17 19:58 的作业 `44078398949`（提交 `c0d3a81`）仍然没有通过。llvm-lto 已经跑完，pgvector 装上了，最后一步对 `apk info -v` 的文本格式核对失败，镜像没留下。Web 已经装上 Alpine 的 nginx `1.28.3-r7`，构建期的 `nginx -t` 因为 `proxy_pass` 里的主机名 `server` 在构建环境里不存在而失败。这不是漏洞扫描结果。

```bash
gh workflow run candidate-images.yml --ref <分支>
```

## 回退

默认 compose 和离线包清单还没改。回退就是继续使用 `pgvector/pgvector:0.8.7-pg17` 和 `chrislusf/seaweedfs:4.52`。不要把候选标签写进已发布的离线包。

## 后续补丁

1. 更换基础 digest、gosu 标签或 pgvector 标签时，同时更换校验和，并重新跑本作业。
2. 五张候选的 HIGH/CRITICAL 都是 0 之后，才把标签写入 compose 和 `pack-offline.sh`，然后跑备份恢复、旧包升级、失败回退、S3 和 D-50。
3. 扫描没有全部为 0 时，保持 #82 不合并，按 CVE、路径和修复版本报告，不新增豁免。
