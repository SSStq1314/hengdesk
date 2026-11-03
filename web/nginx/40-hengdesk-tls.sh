#!/bin/sh
# 挂载了证书和私钥则启用 HTTPS 配置，否则保持仅 HTTP。
# HENGDESK_HTTP_REDIRECT=false：明文端口不再跳转，只保留 /healthz，其余返回 404（D-34 第 2 点）。
# HENGDESK_HTTPS_PORT：跳转目标端口，默认 443（443 时不写端口）。
set -e
if [ -s /etc/hengdesk/tls/tls.crt ] && [ -s /etc/hengdesk/tls/tls.key ]; then
  conf=/etc/nginx/conf.d/default.conf
  cp /etc/nginx/hengdesk/https.conf "$conf"
  port="${HENGDESK_HTTPS_PORT:-443}"
  case "$port" in ''|*[!0-9]*) echo "hengdesk: HENGDESK_HTTPS_PORT 无效：$port" >&2; exit 1 ;; esac
  if [ "${HENGDESK_HTTP_REDIRECT:-true}" = false ]; then
    sed -i 's|return 301 https://\$host\$request_uri;|return 404;|' "$conf"
    echo "hengdesk: 已检测到证书，启用 HTTPS 443；HTTP 跳转已关闭"
  else
    if [ "$port" != 443 ]; then sed -i "s|https://\$host\$request_uri|https://\$host:$port\$request_uri|" "$conf"; fi
    echo "hengdesk: 已检测到证书，启用 HTTPS 443；HTTP 跳转到端口 $port"
  fi
else
  cp /etc/nginx/hengdesk/http.conf /etc/nginx/conf.d/default.conf
  echo "hengdesk: 未检测到证书，仅 HTTP"
fi
