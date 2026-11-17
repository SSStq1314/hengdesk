# 自有镜像 hengdesk/postgres。不使用 pgvector/pgvector 这个名字。
# 不重编 PostgreSQL。基础是官方 postgres:17-alpine 的 linux/amd64 manifest。
# 只替换官方镜像里用 Go 1.24.6 编译的 gosu，并在同一套 PG17 头文件上编译 pgvector 0.8.7。
# gosu 不使用 -ldflags=-s -w，保留 Go buildinfo，供漏洞扫描读取标准库版本。

ARG POSTGRES_IMAGE=postgres:17-alpine@sha256:005c5c37db96c1e6652a79ea31f9886402fba81ac95dc2db897739121dd7f8f2
ARG GO_IMAGE=golang:1.26.11-alpine@sha256:f496f8c537ed506626ee1a6913afb32c45068a48697163c088f604cc775a0c38

FROM ${GO_IMAGE} AS gosu
ARG GOSU_VERSION=1.20
ARG GOSU_SHA256=207a2045e890d3ca3326027c5f585733085960b401a49dbc00e3bb628c054cf8
RUN apk add --no-cache ca-certificates
WORKDIR /src
ADD https://github.com/tianon/gosu/archive/refs/tags/${GOSU_VERSION}.tar.gz /tmp/gosu.tar.gz
RUN echo "${GOSU_SHA256}  /tmp/gosu.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/gosu.tar.gz -C /src --strip-components=1 \
 && GOTOOLCHAIN=local CGO_ENABLED=0 go build -trimpath -o /out/gosu . \
 && cp LICENSE /out/gosu-LICENSE

FROM ${POSTGRES_IMAGE} AS ext
ARG PGVECTOR_VERSION=0.8.7
ARG PGVECTOR_SHA256=cac0b10c360f05b2d521200105ba3697e773d4cd3731f5a915a7e37ebe0bea85
# 官方 PG17 Alpine 用 clang-21 生成扩展 bitcode。只补这个编译器，不重编 PostgreSQL。
RUN apk add --no-cache --virtual .build build-base clang21
WORKDIR /src
ADD https://github.com/pgvector/pgvector/archive/refs/tags/v${PGVECTOR_VERSION}.tar.gz /tmp/pgvector.tar.gz
RUN echo "${PGVECTOR_SHA256}  /tmp/pgvector.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/pgvector.tar.gz -C /src --strip-components=1 \
 && make OPTFLAGS="" \
 && make install \
 && mkdir -p /out/lib /out/ext \
 && cp /usr/local/lib/postgresql/vector.so /out/lib/vector.so \
 && cp /usr/local/share/postgresql/extension/vector.control /out/ext/ \
 && cp /usr/local/share/postgresql/extension/vector--*.sql /out/ext/ \
 && cp LICENSE /out/pgvector-LICENSE \
 && apk del .build

FROM ${POSTGRES_IMAGE}
ARG POSTGRES_IMAGE
ARG PGVECTOR_VERSION=0.8.7
ARG GOSU_VERSION=1.20
ARG GO_IMAGE
LABEL org.opencontainers.image.title="hengdesk-postgres" \
      org.opencontainers.image.licenses="PostgreSQL AND Apache-2.0" \
      hengdesk.image.name="hengdesk/postgres" \
      hengdesk.base="${POSTGRES_IMAGE}" \
      hengdesk.pgvector.version="${PGVECTOR_VERSION}" \
      hengdesk.gosu.version="${GOSU_VERSION}" \
      hengdesk.go.image="${GO_IMAGE}"
COPY --from=gosu /out/gosu /usr/local/bin/gosu
COPY --from=gosu /out/gosu-LICENSE /usr/local/share/hengdesk/licenses/gosu-LICENSE
COPY --from=ext /out/lib/vector.so /usr/local/lib/postgresql/vector.so
COPY --from=ext /out/ext/ /usr/local/share/postgresql/extension/
COPY --from=ext /out/pgvector-LICENSE /usr/local/share/hengdesk/licenses/pgvector-LICENSE
RUN chmod 755 /usr/local/bin/gosu \
 && test -s /usr/local/lib/postgresql/vector.so \
 && test -s /usr/local/share/postgresql/extension/vector.control \
 && test -s /usr/local/share/hengdesk/licenses/gosu-LICENSE \
 && test -s /usr/local/share/hengdesk/licenses/pgvector-LICENSE
