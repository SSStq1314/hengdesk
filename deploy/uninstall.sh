#!/usr/bin/env bash
# 卸载本机衡台 Compose 部署。默认只停止并删除容器；加 --volumes 才删命名卷。
# secrets/ 与宿主机备份目录默认保留。
set -euo pipefail
cd "$(dirname "$0")"
VOLUMES=0
YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    --volumes) VOLUMES=1 ;;
    -y|--yes) YES=1 ;;
    -h|--help) echo "用法：./uninstall.sh [-y] [--volumes]"; exit 0 ;;
    *) echo "未知参数：$1" >&2; exit 1 ;;
  esac
  shift
done
[ -f .env ] || { echo "缺少 .env，无部署可卸载" >&2; exit 1; }
if [ "$YES" != 1 ]; then
  read -r -p "确认卸载衡台容器$([ "$VOLUMES" = 1 ] && echo '及数据卷')？(y/N) " a
  [ "$a" = y ] || [ "$a" = Y ] || { echo "已取消"; exit 1; }
fi
if [ "$VOLUMES" = 1 ]; then
  docker compose down --volumes --remove-orphans
  echo "已删除容器与命名卷；secrets/ 与宿主机备份仍保留。"
else
  docker compose down --remove-orphans
  echo "已删除容器；数据卷、secrets/、备份保留。彻底清除请加 --volumes。"
fi
