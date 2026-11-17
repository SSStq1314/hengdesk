#!/bin/sh
# 只在空数据目录第一次初始化时执行。用来区分本镜像创建的数据目录和旧的 Debian pgvector 数据目录。
set -eu
printf '%s\n' 'hengdesk/postgres' > "$PGDATA/hengdesk-image"
