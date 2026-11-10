# 离线安装（x86_64 · FR-35 / AC-01）

## 打包（有网构建机）

```bash
./deploy/pack-offline.sh --version 0.1.0
# 产物：dist/hengdesk-offline-0.1.0-amd64.tar.gz（勿提交仓库）
```

可选 `--with-minio` 打入 Chainguard MinIO 镜像（AGPL，见 D-28）。

## 安装（无公网目标机）

前置：Docker + Compose v2，x86_64，建议 ≥20 GiB 可用磁盘。

```bash
tar -xzf hengdesk-offline-*-amd64.tar.gz
cd hengdesk-offline-*-amd64
cd deploy
./offline-check.sh
./install.sh --offline --up   # 或非交互加 --non-interactive --ack-master-key --admin-password ...
```

`install.sh --offline` 会：

1. 运行自检；
2. `docker load` 包内 `images/*.tar`；
3. 写入 `.env`（`HENGDESK_VERSION` 取自 `VERSION` 或 `--version`）；
4. `docker compose up -d --no-build --pull never`（不构建、不拉取）。

## 升级

1. 解压新包并 `docker load -i images/*.tar`；
2. 改现有 `deploy/.env` 的 `HENGDESK_VERSION`；
3. `./upgrade.sh`（**不要** `--build`）。

## 卸载

```bash
./uninstall.sh          # 只删容器
./uninstall.sh --volumes  # 连同命名卷；secrets/ 仍保留
```

## CI

`断网安装冒烟（AC-01）` job：构建并 `docker save` 后删除本地应用镜像，在 `HTTP(S)_PROXY` 黑洞下执行 `--offline --up`，断言 healthz 与登录成功。
