# HTTPS / TLS（D-30）

服务本身不终止 TLS，有两种部署方式：

1. **导入证书（nginx 终止 TLS）**
   ```bash
   cd deploy
   ./install.sh -y --tls-cert /path/fullchain.pem --tls-key /path/privkey.pem
   docker compose up -d --build
   ```
   - install.sh 校验 PEM 格式、证书与私钥是否匹配，复制到 `deploy/tls/`（私钥 600，目录 700，已加入 .gitignore）。
   - nginx 监听 443（TLS 1.2/1.3，HSTS）；宿主机 80 和 `HENGDESK_HTTP_PORT`（默认 8080）一律 301 到 `https://<主机>/`，`/healthz` 例外。
   - `.env` 写入 `HENGDESK_COOKIE_SECURE=true`、`HENGDESK_PUBLIC_HTTPS=true`，会话 / CSRF / OIDC state cookie 带 Secure。
   - 换证书：替换 `deploy/tls/tls.crt`、`tls.key` 后执行 `docker compose restart web`。
2. **前置负载均衡终止 TLS**：保持 HTTP 8080，在 `.env` 设置 `HENGDESK_COOKIE_SECURE=true`（`HENGDESK_PUBLIC_HTTPS` 默认跟随它）。

## 后端如何判断 HTTPS
`GET /api/v1/admin/system/status` 返回 `https`：`HENGDESK_PUBLIC_HTTPS=true`，或请求本身是 TLS，或 TCP 对端在 `HENGDESK_TRUSTED_PROXIES` 内且 `X-Forwarded-Proto: https`。不可信来源带的 `X-Forwarded-Proto` 忽略。为 false 时管理后台常驻提示「当前未启用 HTTPS」。

## 对象存储端口
SeaweedFS（8333）/ MinIO（9000）固定只绑 `127.0.0.1`，端口号可用 `HENGDESK_S3_PORT` 改，地址不可改。
