#!/usr/bin/env bash
# 卸载本机衡台 Compose 部署。默认只停止并删除容器；加 --volumes 才删命名卷。
# secrets/ 始终保留（D-36 主密钥在里面，须与数据库备份分开保管）。
set -euo pipefail
cd "$(dirname "$0")"
VOLUMES=0
YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    --volumes) VOLUMES=1 ;;
    -y|--yes) YES=1 ;;
    -h|--help)
      echo "用法：./uninstall.sh [-y|--yes] [--volumes]"
      echo "  默认只删除容器，保留数据卷与 secrets/。"
      echo "  --volumes  删除命名卷（会清除数据库等数据）；secrets/ 仍保留。"
      echo "  --yes      非交互确认。非交互模式必须带此参数，否则退出码非 0。"
      exit 0
      ;;
    *) echo "未知参数：$1" >&2; exit 1 ;;
  esac
  shift
done
[ -f .env ] || { echo "缺少 .env，无部署可卸载" >&2; exit 1; }

if [ "$VOLUMES" = 1 ]; then
  echo "将删除命名卷，数据库等数据会被清除。请先做好备份。"
  echo "secrets/（含 D-36 主密钥）将保留，请与数据库备份分开保管。"
fi

if [ "$YES" != 1 ]; then
  if [ ! -t 0 ]; then
    echo "非交互模式必须加 --yes 才执行卸载。加 --volumes 前请先备份。" >&2
    exit 1
  fi
  if [ "$VOLUMES" = 1 ]; then
    read -r -p "已备份？确认删除衡台容器及数据卷（secrets/ 保留）？(y/N) " a
  else
    read -r -p "确认卸载衡台容器（数据卷与 secrets/ 保留）？(y/N) " a
  fi
  [ "$a" = y ] || [ "$a" = Y ] || { echo "已取消"; exit 1; }
fi

if [ "$VOLUMES" = 1 ]; then
  docker compose down --volumes --remove-orphans
  echo "已删除容器与命名卷；secrets/ 与宿主机备份仍保留。"
else
  docker compose down --remove-orphans
  echo "已删除容器；数据卷、secrets/、备份保留。彻底清除请加 --volumes。"
fi
