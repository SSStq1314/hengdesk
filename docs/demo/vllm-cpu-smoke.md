# vLLM CPU 协议冒烟记录（D-47 / D-49）

按 D-49，通义真实冒烟已从 M1 验收项中移除；**vLLM CPU 小模型的真实协议冒烟仍保留在 M1**。本记录对应：官方 CPU 镜像 + 约 0.5B 小模型的 OpenAI 兼容协议冒烟，加上仓库内既有 vLLM 契约测试。客户现场的通义真实冒烟见 `docs/demo/poc-delivery.md`。

## 契约测试（不依赖实跑）

`server/internal/gateway/contract_vendors_test.go` 已覆盖 vLLM 非流式 / 流式 / 错误 / 超时，回放带为文档构造（`testdata/vllm_cassette.json`），标注「待真实录制替换」。随常规 `go test` / CI 跑，不读取真实密钥。

## 实跑方式

GitHub Actions 工作流「vLLM CPU 协议冒烟」（`.github/workflows/vllm-cpu-smoke.yml`）在托管 `ubuntu-24.04` x86_64 runner 上执行 `tests/acceptance/vllm-cpu-smoke.sh`。该 runner 是云 VM，不是物理机，也不是客户现场。

脚本只拉官方镜像 `vllm/vllm-openai-cpu:latest-x86_64`。CPU 上用 `--dtype float32`（bfloat16 在无 GPU 的 CPU 镜像上常被拒绝）。先启动 `Qwen/Qwen2.5-0.5B-Instruct`；若未就绪，再在**同一 vLLM 镜像**上试 `HuggingFaceTB/SmolLM2-135M-Instruct`。不使用 Ollama，也不把 mock 当成模型。通过条件：`/v1/models` 就绪、非流式返回 `chat.completion` 和 usage、流式含 `data:`、`[DONE]`、增量 content，且请求了 `stream_options.include_usage` 时流里带 usage。

证据在该 workflow 的 artifact `vllm-cpu-smoke-evidence`：宿主与镜像摘要、命令、脱敏日志、原始 `/v1/models`、非流式 JSON、流式 SSE，以及证据包 SHA256。密钥不应出现；脚本会把 `Authorization: Bearer` 打成 `[REDACTED]`。资源不足或镜像拉取失败时 job 失败，结果记为未验证，不能写成通过。

本机有 Docker 时：

```bash
./tests/acceptance/vllm-cpu-smoke.sh
```

## 实跑记录

- 2026-11-09：共享开发机没有 Docker，未实跑。
- 2026-11-11：改为上述 GitHub 托管 x86 runner。具体镜像摘要、模型、通过与否和证据哈希以本 PR 的 Actions 运行和 artifact 为准，在运行完成前不记为通过。这不是通义实测。
