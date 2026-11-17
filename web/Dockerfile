FROM node:22-alpine AS build
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY . .
RUN pnpm build

FROM nginx:1.28-alpine
# 官方镜像把 nginx.org 的包写进 /etc/apk/world，钉成 nginx=1.28.3-r1。
# apk upgrade 会升 Alpine 自己的包，但不会动这行，扫描仍停在 r1。
# 卸掉 nginx.org 的包，改用同一 Alpine 3.23 分支里已发布的 nginx（修复线是 1.28.3-r6，当前仓库是 r7）。
# Alpine 的站点文件在 http.d；conf.d 位于根上下文，不能放 server 块。
RUN set -eux \
 && apk del --no-cache \
      nginx \
      nginx-module-acme \
      nginx-module-geoip \
      nginx-module-image-filter \
      nginx-module-njs \
      nginx-module-xslt \
 && apk upgrade --no-cache \
 && apk add --no-cache 'nginx>=1.28.3-r6' \
 && rm -f /etc/nginx/http.d/default.conf \
 && mkdir -p /etc/nginx/conf.d /etc/nginx/modules /etc/nginx/http.d \
 && printf '# hengdesk: 站点在 http.d，这个文件只是让根上下文的 include 有匹配。\n' > /etc/nginx/conf.d/00-hengdesk.conf \
 && if ! ls /etc/nginx/modules/*.conf >/dev/null 2>&1; then \
      printf '# hengdesk: 未装动态模块。\n' > /etc/nginx/modules/00-hengdesk.conf; \
    fi \
 && nginx_ver=$(apk info -v | sed -n 's/^nginx-//p' | head -1) \
 && case "$(apk version -t "$nginx_ver" 1.28.3-r6)" in \
      '>'|'=') ;; \
      *) echo "nginx ${nginx_ver} 低于 1.28.3-r6" >&2; exit 1 ;; \
    esac \
 && nginx -v
COPY nginx/http.conf nginx/https.conf nginx/proxy.inc /etc/nginx/hengdesk/
COPY nginx/40-hengdesk-tls.sh /docker-entrypoint.d/
COPY nginx/http.conf /etc/nginx/http.d/default.conf
COPY --from=build /src/dist /usr/share/nginx/html
RUN nginx -t
EXPOSE 80 443
