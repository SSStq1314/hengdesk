# 衡台 HengDesk

衡台 · 企业 AI 办公平台（私有化部署，单机 Docker Compose）。

## 目录结构

| 目录 | 说明 |
|---|---|
| `server/` | Go 1.26（工具链锁定 go1.26.10，见 D-29）后端，单二进制 `hengdesk`，子命令 `server` / `worker` / `migrate`（Echo v4 + ent + asynq） |
| `web/` | 前端，基于 [satnaing/shadcn-admin](https://github.com/satnaing/shadcn-admin)（MIT）裁剪 |
| `deploy/` | Docker Compose 部署、安装脚本 |
| `tests/` | 集成 / 冒烟测试 |
| `docs/` | 设计文档、决策记录 |

## 快速开始

```bash
make build && ./bin/hengdesk server   # 默认监听 :8080，GET /healthz
make test
```

配置全部来自 `HENGDESK_*` 环境变量，见 `server/internal/config`。

## 许可

当前暂放 Apache-2.0（见 LICENSE）。**社区版最终许可（Apache-2.0 / MIT）待产品与法务最终确认**，参见决策 D-17。
本项目只复用 MIT / Apache-2.0 代码；AGPL / GPL / 附加条款项目（MonkeyCode、MaxKB、Dify、FastGPT 等）仅参考设计，不复制代码、不引入依赖（D-07）。

## 第三方代码

- `web/` 基于 [satnaing/shadcn-admin](https://github.com/satnaing/shadcn-admin)（MIT，Copyright (c) 2024 Sat Naing），保留其 `web/LICENSE`，见 `web/NOTICE`。

## 快速部署（Docker Compose）

```bash
cd deploy
./install.sh                     # 交互：选择 SeaweedFS（默认）/ MinIO / 外部 S3
# 或非交互：./install.sh -y --storage seaweedfs
docker compose up -d --build
```

浏览器访问 `http://<主机>:8080`，管理员邮箱默认 `admin@hengdesk.local`，密码见 `deploy/.env` 中的 `HENGDESK_ADMIN_PASSWORD`（install.sh 随机生成）。
