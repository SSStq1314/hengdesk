FROM golang:1.26.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
# 模块代理偶发 INTERNAL_ERROR。失败再取一次，避免整次打包扫描被这一下打断。
RUN go mod download || go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/hengdesk ./cmd/hengdesk

FROM alpine:3.22
# postgresql17-client：migrate 升级前用 pg_dump 自动备份（D-31）
RUN apk add --no-cache ca-certificates tzdata postgresql17-client && adduser -D -u 10001 hengdesk \
 && mkdir -p /backups && chown hengdesk:hengdesk /backups
COPY --from=build /out/hengdesk /usr/local/bin/hengdesk
USER hengdesk
EXPOSE 8080
ENTRYPOINT ["hengdesk"]
CMD ["server"]
