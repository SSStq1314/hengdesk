# HTTPS / TLS（D-30）

服务本身不终止 TLS，有两种部署方式：

1. **导入证书（nginx 终止 TLS）**
   ```bash
   cd deploy
   ./install.sh -y --tls-cert /path/fullchain.pem --tls-key /path/privkey.pem
   docker compose up -d --build
   ```
   - install.sh 校验 PEM 格式、证书与私钥是否匹配，复制到 `deploy/tls/`（私钥 600，目录 700，已加入 .gitignore）。
   - nginx 监听 443（TLS 1.2/1.3，HSTS）；默认宿主机 80 和 `HENGDESK_HTTP_PORT`（默认 8080）一律 301 到 `https://<主机>/`，`/healthz` 例外。
   - 端口（D-34 第 2 点，默认值与原行为一致）：

     | install.sh 参数 | .env 变量 | 默认 | 说明 |
     |---|---|---|---|
     | `--https-port` | `HENGDESK_HTTPS_PORT` | 443 | 宿主机 HTTPS 端口；非 443 时跳转地址带端口 |
     | `--redirect-port` | `HENGDESK_REDIRECT_PORT` | 80 | 额外占用、用于跳转的 HTTP 端口（`docker-compose.tls-redirect.yml`） |
     | `--no-http-redirect` | `HENGDESK_HTTP_REDIRECT=false` | 跳转开启 | 关闭跳转：不加载 `docker-compose.tls-redirect.yml`，不占用 80；`HENGDESK_HTTP_PORT` 上只保留 `/healthz`，其余 404 |

     例：80 被占用时 `./install.sh -y --tls-cert … --tls-key … --redirect-port 8880`；完全不要明文跳转时加 `--no-http-redirect`。
   - 旧版 install.sh 生成的 HTTPS `.env` 在执行 `upgrade.sh` 时会自动补上跳转配置，保持 80 跳转。
   - `.env` 写入 `HENGDESK_COOKIE_SECURE=true`、`HENGDESK_PUBLIC_HTTPS=true`，会话 / CSRF / OIDC state cookie 带 Secure。
   - 换证书：替换 `deploy/tls/tls.crt`、`tls.key` 后执行 `docker compose restart web server`（server 只读挂载证书文件，用于后台提示）。
2. **前置负载均衡终止 TLS**：保持 HTTP 8080，在 `.env` 设置 `HENGDESK_COOKIE_SECURE=true`（`HENGDESK_PUBLIC_HTTPS` 默认跟随它）。

## 自签证书（D-34 第 3 点）
没有正式证书时可让 install.sh 生成自签证书（默认不生成）：
```bash
./install.sh -y --self-signed --san hengdesk.corp.local --san 10.0.0.8
```
- RSA 2048、SHA-256、有效期 365 天；`--san` 可重复，自动区分域名（DNS）与 IP；缺省为 `localhost` 与本机主机名，CN 取第一个 SAN。
- 与 `--tls-cert/--tls-key` 互斥；交互模式下证书路径输入 `self-signed` 即可。`.env` 记录 `HENGDESK_TLS_SELF_SIGNED=true`。
- 浏览器不信任自签证书：客户端可导入 `deploy/tls/tls.crt`。
- 管理后台：证书为自签时常驻提示「当前为自签证书」；任何证书在到期前 30 天起提示剩余天数，过期后提示已过期。
- 证书信息：`GET /api/v1/admin/system/tls`（需系统设置权限）返回 `present / self_signed / subject / sans / not_after / days_left / expiring / expired`；`/admin/system/status` 的 `tls` 字段相同。server 通过 `HENGDESK_TLS_CERT_FILE`（默认 `/etc/hengdesk/tls/tls.crt`）只读读取证书，不接触私钥。
- 续期：重新执行 `./install.sh … --self-signed --force`，再 `docker compose up -d`。

## 后端如何判断 HTTPS
`GET /api/v1/admin/system/status` 返回 `https`：`HENGDESK_PUBLIC_HTTPS=true`，或请求本身是 TLS，或 TCP 对端在 `HENGDESK_TRUSTED_PROXIES` 内且 `X-Forwarded-Proto: https`。不可信来源带的 `X-Forwarded-Proto` 忽略。为 false 时管理后台常驻提示「当前未启用 HTTPS」。

## 对象存储端口
SeaweedFS（8333）/ MinIO（9000）固定只绑 `127.0.0.1`，端口号可用 `HENGDESK_S3_PORT` 改，地址不可改。
