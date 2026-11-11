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
- `66f57ed` 上的 [run 43053476770](https://github.com/SSStq1314/hengdesk/actions/runs/43053476770) 是更早一次通过。#78 合入前 head `b0b6a940b7ba33825882305a027efb8e84d03243` 的 [run 43110669860](https://github.com/SSStq1314/hengdesk/actions/runs/43110669860) 也已 `result=pass`，证据包 SHA256 `760565b577470c5b4733a69a62fae85d02976808e9056f89cc1fb952c7913c2e`。
- 最终代码树是 main `1ae07162dd209bce1c794347fc3e0ef92da381c7`（与 `9c109cc3d3940956fff8a4bba86c001d0d0e1f57` 同树）。该树上的运行是 [43114580548](https://github.com/SSStq1314/hengdesk/actions/runs/43114580548)，`result=pass`，`finished_utc=2026-11-11T22:51:00Z`。证据包 SHA256 `9d4ba3fdbc22e14f2414c116d298209f47f3578aa4588788cb22b85499197c31`。镜像 `vllm/vllm-openai-cpu:latest-x86_64`，模型 `Qwen/Qwen2.5-0.5B-Instruct`，`--dtype float32`。非流式 `usage.total_tokens=38`（prompt 36 / completion 2）。这是 GitHub 托管 x86 云 VM 上的 vLLM CPU 协议冒烟。Ollama 未使用。通义真实调用不在这次运行里。

下面这张表只属于更早的提交 `66f57ed`。最终 head 的验收证据是上一段的 run 43114580548。

| 项 | 值 |
|---|---|
| 提交 | `66f57ed2cd996b3e709678abe16fa28adda2871d`（先前一次，不是最终 head） |
| 运行 | https://github.com/SSStq1314/hengdesk/actions/runs/43053476770 |
| 主机 | `runnervm7aw5e`，内核 `7.0.0-1017-azure`，Hypervisor Microsoft，AMD EPYC 7763（虚拟机内 4 vCPU），内存 15 GiB，根盘 145G |
| Docker | Engine 28.0.4 |
| 镜像 | `vllm/vllm-openai-cpu:latest-x86_64` |
| 镜像摘要 | `sha256:4154467a886f8b7ffa9dbab32f873f5ca4528409d042a6ec866e8888223263d3`（约 5.2 GiB，创建时间 2026-11-01） |
| 模型 | `Qwen/Qwen2.5-0.5B-Instruct`（未改用更小模型，也未使用 Ollama） |
| 参数 | `--dtype float32 --max-model-len 512 --max-num-seqs 1 --enforce-eager` |
| 非流式 | HTTP 200，`object=chat.completion`，`finish_reason=stop`，`usage.total_tokens=38`（prompt 36 / completion 2） |
| 流式 | HTTP 200，含 `data:`、`[DONE]`、增量 content，以及 `include_usage` 带来的 usage |
| 证据包 SHA256 | `0492aa95905e3348135feeec6dcdfedbb4c5a9735f9c6eb99b11d9df4991df66`（`vllm-cpu-smoke-evidence.tar.gz`） |
| artifact zip SHA256 | `a0743ea2fafedbc30d593fc3a36951ec9a67320cf9b401d9fb25ff8b6d810219` |

原始 `/v1/models`、非流式 JSON、流式 SSE 和脱敏容器日志在该 run 的 artifact `vllm-cpu-smoke-evidence` 里，不写入本仓库。没有使用真实 API 密钥。
