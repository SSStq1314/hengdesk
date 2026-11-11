# 正式离线包干净断网安装记录（AC-01 / D-46）

本项与真机界面演示中「执行环境没有 Docker，AC-01 未演示」是**同一条** M1 验收缺口，不分开跟踪。要求：用**正式发布的离线包**（`deploy/pack-offline.sh` 默认产物，不是验收包、也不是 CI 临时 `docker save` 目录）在一台**干净、断网**的机器上完整安装一次，并留下记录。

按 D-48：该正式包必须基于 **`bf4d437` 或更新的提交** 构建；安装与验收必须在**新初始化的数据库**上做。不为旧审计链做迁移——若机器上有更早构建留下的数据卷，先卸载并删除卷（`uninstall.sh --yes --volumes`），再装新包。

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

D-50（2026-11-11）把「全新 GitHub 托管 x86 VM」定为可以关闭 D-46 第 8 条的实测环境。记录范围是该云 VM。物理机、客户现场和其他架构不在这次记录里。最终代码树（main `1ae07162dd209bce1c794347fc3e0ef92da381c7`，与 PR head `9c109cc3d3940956fff8a4bba86c001d0d0e1f57` 同树）上的运行是 [43114580527](https://github.com/SSStq1314/hengdesk/actions/runs/43114580527)，`result=pass`。证据包 SHA256 `99416482f7a334d2b2cadc61d6a6359c3cbb5b5db10426b3037f93cd09f54a3e`。正式包 SHA256 `b6bee720e327ca840def8d021ea431c19a8fb1aec98f4ac891464146aff14c3b`。容器 IPv6 被内核报 `Network is unreachable`，`HD_DOCK6` 探测前后都是 0，没有报文计数。变基前的运行 [43088030693](https://github.com/SSStq1314/hengdesk/actions/runs/43088030693) 是更早一次通过，证据包 SHA256 `422b79adcca8a4d1dc7f951856b02dbe76898de701fb776318db9b4de7de7d2c`，不作为最终 head 的证据。取证脚本是 `tests/acceptance/ci/d50-offline-install.sh`。它和下面两件旧记录不是同一件事：

- 「断网安装冒烟」用的是 CI 临时 `docker save` 目录。
- 「断网 15 步验收」用的是带 mocks 的验收包。

D-50 要求默认正式包、隔离之后才 load 产品镜像、清洁基线、宿主 netns 与容器的 IPv4/IPv6/DNS 失败探针，以及重启后的数据持久性。控制通道只留 runner cgroup 在 init 网络命名空间里的 GitHub 连接；安装进程和产品容器不能借用。证据在该 workflow 的 artifact `d50-offline-evidence`。上面的最终运行已 `result=pass`。资源不足或 job 失败的另一次运行仍记为未验证。

## 验收时请补做的人工步骤

在一台安装了 Docker Compose v2 的 **x86_64 干净机器**上（建议新装系统或至少没有预先 `docker pull` 衡台镜像），**断开公网或保证无法拉取外网镜像**，然后：

1. 拷入官方发布的 `hengdesk-offline-<版本>-amd64.tar.gz`（默认正式包）。
2. 按 `docs/ops/offline-install.md` / `docs/demo/m1-acceptance.md` 节 A 解压、`./offline-check.sh`、`./install.sh --offline --up`。
3. 记录：日期与时区（America/Los_Angeles 或同时注明机器本地时区）、机器概要（OS、磁盘、是否全新）、包文件名与版本、**构建所用 git 提交（须 ≥ `bf4d437`）**、确认为新初始化的空库、healthz、管理员登录是否成功、安装期间是否出现 `docker pull` / 外连失败或成功。
4. 把记录追加到本文件或作为附件归档到验收材料，注明操作人。
5. （可选）再在同机或另一台断网机上解压验收包，跑 `tests/acceptance/run.sh --compose --offline`，把 15 步通过与否一并记下。

M1 对照表里，AC-01 / D-46 安装项对这次 GitHub 托管 x86 云 VM 记为通过。上面的人工步骤留给以后若要在物理机或客户现场另做记录时使用。容器 IPv6 在最终证据里是内核不可达，`HD_DOCK6` 没有计数。
