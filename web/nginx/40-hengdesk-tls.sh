#!/bin/sh
# 挂载了证书和私钥则启用 HTTPS 配置，否则保持仅 HTTP。
set -e
if [ -s /etc/hengdesk/tls/tls.crt ] && [ -s /etc/hengdesk/tls/tls.key ]; then
  cp /etc/nginx/hengdesk/https.conf /etc/nginx/conf.d/default.conf
  echo "hengdesk: 已检测到证书，启用 HTTPS 443"
else
  cp /etc/nginx/hengdesk/http.conf /etc/nginx/conf.d/default.conf
  echo "hengdesk: 未检测到证书，仅 HTTP"
fi
