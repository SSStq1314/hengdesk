FROM node:22-alpine AS build
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY . .
RUN pnpm build

FROM nginx:1.28-alpine
COPY nginx/http.conf nginx/https.conf nginx/proxy.inc /etc/nginx/hengdesk/
COPY nginx/40-hengdesk-tls.sh /docker-entrypoint.d/
COPY nginx/http.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/dist /usr/share/nginx/html
EXPOSE 80 443
