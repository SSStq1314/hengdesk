# 正式离线包干净断网安装记录（AC-01 / D-46）

本项与真机界面演示中「执行环境没有 Docker，AC-01 未演示」是**同一条** M1 验收缺口，不分开跟踪。要求：用**正式发布的离线包**（`deploy/pack-offline.sh` 默认产物，不是验收包、也不是 CI 临时 `docker save` 目录）在一台**干净、断网**的机器上完整安装一次，并留下记录。

## 本执行环境实际做了什么

| 项 | 情况 |
|---|---|
| 日期 | 2026-11-09（America/Los_Angeles）起跟进第八块期间 |
| 机器 | 共享 Linux 开发机（衡台第八块执行环境），**没有 Docker CLI / 容器运行时** |
| 是否干净服务器 | 否：已有其它工作区、数据库端口和本机二进制依赖，不是全新安装的操作系统 |
| 是否断网 | 开发机本身可出网；本环境无法用 Docker 做「代理黑洞 + `--pull never`」的安装演示 |
| 正式包安装 | **未在本机实操**（缺 Docker） |
| 替代记录 | GitHub Actions job「断网安装冒烟（AC-01）」：在 `ubuntu-latest` 上临时 `docker save`、删本地镜像、代理黑洞、`install.sh --offline --up`，验证 healthz 与管理员登录。第八块 PR3 另增 job「断网 15 步验收」：用 `pack-offline.sh --with-acceptance` 的验收包跑完整 15 步，并以 iptables 检查容器无公网出站 |

## 与「真正干净、断网的机器 + 正式发布包」的差异

1. **不是全新机器**：CI 跑在 GitHub 托管的 Ubuntu 上，预装 Docker、构建工具和缓存，不是刚装好操作系统、只放安装包的验收机。
2. **第八块执行环境完全没有 Docker**，既不能打包也不能安装，比 CI 还少一层「像目标机」的近似。
3. **冒烟 job 用的是 CI 临时 `docker save` 目录**，不是发布流程里归档的 `hengdesk-offline-<版本>-amd64.tar.gz`（无 `-acceptance` 后缀的默认正式包）。
4. **15 步 job 用的是验收包**（含 `hengdesk/mocks`）。AC-01 / D-46 要求的「正式包」安装记录仍要用默认包单独做一次。
5. CI 的「断网」是安装与脚本运行阶段的代理黑洞、`--pull never` 和（15 步 job 中的）iptables，不是整机拔网线；跑 job 之前的打包步骤是有网的。

因此：CI 绿只能证明「在 Runner 上按同样脚本能离线 load 并启动」，**不能替代**验收时在干净断网机上的正式包安装记录。

## 验收时请补做的人工步骤

在一台安装了 Docker Compose v2 的 **x86_64 干净机器**上（建议新装系统或至少没有预先 `docker pull` 衡台镜像），**断开公网或保证无法拉取外网镜像**，然后：

1. 拷入官方发布的 `hengdesk-offline-<版本>-amd64.tar.gz`（默认正式包）。
2. 按 `docs/ops/offline-install.md` / `docs/demo/m1-acceptance.md` 节 A 解压、`./offline-check.sh`、`./install.sh --offline --up`。
3. 记录：日期与时区（America/Los_Angeles 或同时注明机器本地时区）、机器概要（OS、磁盘、是否全新）、包文件名与版本、healthz、管理员登录是否成功、安装期间是否出现 `docker pull` / 外连失败或成功。
4. 把记录追加到本文件或作为附件归档到验收材料，注明操作人。
5. （可选）再在同机或另一台断网机上解压验收包，跑 `tests/acceptance/run.sh --compose --offline`，把 15 步通过与否一并记下。

在上述记录完成之前，AC-01 / D-46 安装项在对照表中保持「未覆盖 / 须补做」。
