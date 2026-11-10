# M1 端到端验收脚本（第八块）

按 PRD §13 的 15 步演示剧本，对一台已安装的衡台实例发 HTTP 请求并断言 AC-01～AC-16 与 D-46 相关行为。CI 只用 mock 上游，不读取真实密钥。

## 组成

- `run.sh`：入口。可叠加 `deploy/docker-compose.acceptance.yml` 拉起 mockupstream / mockmcp / mockoidc，再在同一网络里跑 `m1accept`。
- `server/cmd/m1accept`：15 步 Go 脚本。
- `server/cmd/mockoidc`、`mockupstream -demo-script`、`mockmcp`（`/calls` 供 AC-06 计数）、`mockredir`（第 12 步：白名单内端点 307 到白名单外，复用真机演示 redirector 思路）。
- `server/Dockerfile.mocks`：把上述二进制打成 `hengdesk/mocks` 镜像（不进入默认生产离线包）。
- `testdata/`：部门 CSV 与三份知识库样例（高管文件含标记 `HD-EXEC-SECRET-991`）。

## 本地（已有 `deploy/.env`）

```bash
./tests/acceptance/run.sh --compose
```

会构建 mocks 镜像、启动覆盖件、重启应用以加载 OIDC，然后跑完整 15 步。AC-08 默认尝试 `docker compose restart server`（从宿主机传入命令；容器内若无 docker 套接字则跳过或由 CI 另外注入）。

若只想对已启动实例、且用本地密码代替 SSO：

```bash
./tests/acceptance/run.sh --base-url http://127.0.0.1:8080 --admin-password '…' --skip-oidc --skip-restart
```

## 离线包

默认 `pack-offline.sh` 不含 mocks。带验收依赖请用 `--with-acceptance`（PR3 落地）。手册见 `docs/demo/m1-acceptance.md`。

## 硬规则

- 越权返回 404，未登录返回 401。
- 审计 fail-closed 由既有集成测试覆盖；本脚本验证审计回放与不可删改。
- D-46：导出 Markdown 必须以 front matter 开头；非法 AIGC 文案保存失败；不能导出他人会话。


## vLLM CPU 协议冒烟（D-47，可选、需出网）

通义真实冒烟挂起至 M2 W1。替代：

```bash
./tests/acceptance/vllm-cpu-smoke.sh
```

用官方镜像 `vllm/vllm-openai-cpu` 与约 0.5B 小模型检查 `/v1/chat/completions` 非流式与流式。不进入断网 CI。记录见 `docs/demo/vllm-cpu-smoke.md`。既有契约测试仍随 `go test ./internal/gateway` 跑。
