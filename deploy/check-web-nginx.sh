#!/usr/bin/env bash
# 在 compose 网络里检查 nginx 配置。上游主机名 server 这时能解析。
# 构建镜像时不做 nginx -t，因为那时没有这个主机名。
set -euo pipefail
cd "$(dirname "$0")"
docker compose exec -T web nginx -t
