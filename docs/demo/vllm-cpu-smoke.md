# vLLM CPU 协议冒烟记录（D-47 / D-49）

按 D-49，通义真实冒烟已从 M1 验收项中移除；**vLLM CPU 小模型的真实协议冒烟仍保留在 M1**。本记录对应：官方 CPU 镜像 + 约 0.5B 小模型的 OpenAI 兼容协议冒烟，加上仓库内既有 vLLM 契约测试。客户现场的通义真实冒烟见 `docs/demo/poc-delivery.md`。

## 契约测试（不依赖实跑）

`server/internal/gateway/contract_vendors_test.go` 已覆盖 vLLM 非流式 / 流式 / 错误 / 超时，回放带为文档构造（`testdata/vllm_cassette.json`），标注「待真实录制替换」。随常规 `go test` / CI 跑，不读取真实密钥。

## 实跑记录

- 日期：2026-11-09 19:24 PST（America/Los_Angeles）
- 环境：衡台第八块执行用共享开发机（Linux，无 Docker 可执行文件，无容器运行时）
- 脚本：`tests/acceptance/vllm-cpu-smoke.sh`
- 计划镜像：`vllm/vllm-openai-cpu:latest-x86_64`
- 计划模型：`Qwen/Qwen2.5-0.5B-Instruct`
- **结果：未实跑**
- 原因：本环境没有 Docker（`docker` 命令不存在），无法拉取官方 CPU 镜像或启动服务。与「有 Docker 的验收机 / 开发机」的差异仅在于此；脚本本身已提交，不把该冒烟加入断网 CI 必跑门禁（需出网拉镜像和权重）。

有 Docker 的机器请执行：

```bash
./tests/acceptance/vllm-cpu-smoke.sh
```

通过后在本文件追加一行：日期、主机、镜像标签、模型、非流式/流式是否通过。不要把密钥、完整模型输出或容器日志里的令牌写入仓库。
