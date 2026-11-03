# D-28 存储与缓存许可

> 与 `ai-office-platform/decisions.md` 中 D-28 内容一致（同步副本）。
## D-28 存储与缓存许可：采纳 Valkey，默认 SeaweedFS，MinIO 降为可选（补充 D-24）
- 决策日期：2026-11-02
- 拍板内容：
  - 缓存与 asynq 队列采用 **Valkey**，compose 固定主版本（`valkey/valkey:8.1.11-alpine`），CI 对真实 Valkey 做 asynq 兼容性冒烟（入队、重试、`ProcessIn` 延时、Scheduler 周期任务）。
  - 对象存储**默认 SeaweedFS**（Apache-2.0，启用 S3 网关）；MinIO 改为可选覆盖文件 `deploy/docker-compose.minio.yml`；另支持外部 S3。代码只依赖 S3 API，endpoint / region / AK/SK / path-style / bucket 均可配置。
  - SeaweedFS 与 MinIO 两套 compose 在 CI 矩阵中跑同一套 S3 集成测试（上传、下载、删除、列表、预签名 URL），均须通过；`deploy/install.sh` 提供 seaweedfs / minio / 外部 S3 三种选择。
  - CI 生成 SBOM（syft，SPDX JSON）作为构建产物。
  - 本条为**产品侧结论，不替代正式法律意见，商用发布前许可清单含 SBOM 须交法务复核**。
- 理由：
  1. 受监管客户的法务对 AGPL 敏感，默认组合中不出现 AGPL 组件可减少采购阶段的许可审查阻力。
  2. 离线安装包属于"分发"，默认不捆绑 AGPL 组件（MinIO 服务端为 AGPL-3.0），可避免由此带来的源码提供义务。
  3. MinIO 社区版发布收缩，存在长期供应风险（已核实）：
     - 2025-10 起 MinIO 社区版改为"仅源码分发"，官方 README 写明不再提供社区版预编译二进制，历史二进制"不再维护、不会更新"，并推荐 AIStor Free / Enterprise 作为替代（[minio/minio README](https://github.com/minio/minio/blob/master/README.md)，对应提交 [9e49d5e](https://github.com/minio/minio/commit/9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a)）。
     - 维护者在 [minio/minio#21647](https://github.com/minio/minio/issues/21647)（2025-10-18、2025-10-23）确认 Docker Hub / Quay 不再发布新镜像，安全版本 `RELEASE.2025-10-15T17-29-55Z` 无官方镜像；社区版"只接收 bug 修复与 CVE 补丁，不再开发新功能"，"决定是最终的"。
     - Chainguard 公告（[Secure and Free MinIO Chainguard Containers](https://www.chainguard.dev/unchained/secure-and-free-minio-chainguard-containers)）称 2025-10-23 起官方停止发布社区版容器镜像，并将其 `cgr.dev/chainguard/minio` 免费提供（免费层只有 latest 标签）。2026-11-02 实测 Docker Hub API 查询 `minio/minio` 仓库返回 "object not found"。
  - Valkey 许可（已核实）：BSD 3-Clause，Linux Foundation 旗下项目，从 Redis 改许可前的开源版本分叉（[valkey-io/valkey](https://github.com/valkey-io/valkey)、[COPYING（8.0 分支）](https://github.com/valkey-io/valkey/blob/8.0/COPYING)、[LF 发布 Valkey 8.0 公告](https://www.linuxfoundation.org/press/valkey-8-0)）。8.1 系列最新补丁 8.1.11 于 2026-10-26 发布，Docker Hub 提供 `valkey/valkey:8.1.11`（[valkey.io/download/8.1](https://valkey.io/download/8.1/)）。
  - SeaweedFS 许可：Apache-2.0（GitHub API `repos/seaweedfs/seaweedfs/license` 返回 `Apache-2.0`），最新版本 4.52（[GitHub Release](https://github.com/seaweedfs/seaweedfs/releases/tag/4.52)）。
- 备选方案：默认 MinIO（AGPL 且官方停止发布镜像，否决）；只支持外部 S3、不内置存储（离线一体化交付体验差，否决，保留为可选项）；Redis（7.4 起非 OSI 许可，否决，见 D-24）。
