# D-35 模型网关客户端（D-24 的补充，产品经理定）

> 同步自 decisions.md（产品经理，2026-11-03，PR #25）。原文如下，实现说明附后。

- D-24 原文不改。实际实现改为用标准库自研精简的 OpenAI 兼容客户端，不依赖 go-openai，原因有三：透传 DeepSeek 的 reasoning_content / thinking 字段；自己控制 SSE 解析；上游报错脱敏后再返回。
- 约束：
  - 不复制第三方代码。
  - 每接一家上游（DeepSeek / 通义 / vLLM）都要有录制回放的契约测试，覆盖流式、思考字段、错误、超时四种情况。
  - 通义和 vLLM 的真实冒烟测试列入 M1 验收清单，没通过不算 M1 完成。

## 实现说明
- 客户端：PR #25 落实（`server/internal/gateway`），仅用标准库，未复制第三方代码。
- DeepSeek 契约测试：M1 第二块 PR2 落实，`server/internal/gateway/contract_deepseek_test.go` 回放
  `testdata/deepseek_cassette.json`，覆盖非流式、流式（含 reasoning_content）、错误（401/429/400，消息脱敏）、超时，随 `go test` 在 CI 运行。
  - 录音由 `tests/spikes/model-compat/make-cassette.py` 从本机真实调用结果生成：不保存任何请求头，生成时断言输出不含 `sk-` 形式的 Key。
  - 仓库与 CI 不读取、不保存 `DEEPSEEK_API_KEY`；重新录制步骤见脚本说明。
- 通义 / vLLM 的契约测试放到第二块 PR5（适配器扩展）；二者的真实冒烟测试已列入 M1 验收清单（见 [m1-block2.md](../progress/m1-block2.md)）。
