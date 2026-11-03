# Spike 4：OpenAI 兼容协议差异矩阵（DeepSeek / 通义千问 DashScope / vLLM / Ollama）

日期：2026-11-02（America/Los_Angeles）　状态：**部分实测**

## 0. 实测范围与可信度标注

| 标记 | 含义 |
|---|---|
| **【实测】** | 本机 `main.go` 实际发请求、保存原始 SSE/JSON（见 `ollama-*.json`） |
| **【文档】** | 仅依据厂商官方文档（2026-11-02 抓取），**未实测** |
| **【待测】** | 文档未说明或疑似过时，必须拿真实 key / GPU 复测 |

- 首轮环境中没有模型 API Key。**2026-11-02 补测：DeepSeek 已用真实 key 实测**（共 51 次 HTTP 调用，`cmd/deepseek`，原始结果见 `deepseek-runs/deepseek-results.json`，Authorization 已脱敏）。DashScope、vLLM 仍未实测。
- 本机无 GPU，**vLLM 未部署**，只能依据文档。
- Ollama 0.40.13（CPU）+ `qwen2.5:1.5b` 为唯一实测后端。1.5B 小模型的工具调用能力弱，结论只反映**协议形态**，不代表生产模型的质量。

## 1. 测试工具

`main.go`（仅标准库，无第三方依赖）。用法：

```bash
go run . -base http://127.0.0.1:11434/v1 -model qwen2.5:1.5b -key x -out ollama.json          # 中文工具提示
go run . -base ... -toolq "Look up ticket T-1 using the tool." -out ollama-en.json            # 英文工具提示
# 拿到 key 后同样可跑：
go run . -base https://api.deepseek.com -model deepseek-chat -key $DEEPSEEK_API_KEY -out deepseek.json
go run . -base https://dashscope.aliyuncs.com/compatible-mode/v1 -model qwen-plus -key $DASHSCOPE_API_KEY -out dashscope.json
go run . -base http://<vllm>:8000/v1 -model Qwen/Qwen2.5-7B-Instruct -out vllm.json
```

用例：`chat`、`chat_stream_no_stream_options`、`chat_stream_include_usage`、`tools`、`tools_choice_required`、`tools_stream_include_usage`。每个用例记录 HTTP 状态、finish_reason、usage 形态、usage 出现在哪个 chunk、是否有 `[DONE]`、TTFT、首尾原始 chunk。

## 2. 差异矩阵

| 维度 | Ollama 0.40.13 | DeepSeek | DashScope 兼容模式 | vLLM |
|---|---|---|---|---|
| Base URL | `http://host:11434/v1`【实测】 | `https://api.deepseek.com`【实测】；`GET /models` 只返回 `deepseek-flash`（V4.1-Flash，可输入图片）、`deepseek-v4-pro`，context 1048576，max_output 393216【实测】；旧名 `deepseek-chat`/`deepseek-reasoner` 仍可用，**分别映射到 flash 非思考 / 思考**，响应里 `model` 回写为 `deepseek-flash`【实测】 | 新：`https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/compatible-mode/v1`；旧 `https://dashscope.aliyuncs.com/compatible-mode/v1` 仍可用；国际站另有域名；**Key 与地域绑定，跨地域 401 invalid_api_key**【文档】 | `http://host:8000/v1`【文档】 |
| 非流式 usage 字段 | `prompt_tokens / completion_tokens / total_tokens / prompt_tokens_details.cached_tokens`【实测】 | 额外 `prompt_cache_hit_tokens`、`prompt_cache_miss_tokens`，同时也有标准 `prompt_tokens_details.cached_tokens`（两者数值一致）；思考模式有 `completion_tokens_details.reasoning_tokens`【实测】。缓存以 128 token 为粒度命中（实测 128/256/384） | 示例只有三项基础字段【文档】，缓存字段【待测】 | 标准三项【文档】 |
| 流式不带 `stream_options` | **无 usage**【实测】 | ⚠️**与文档不同：仍然返回 usage**（在最后一个 chunk 上），思考/非思考均如此【实测】 | 无 usage【文档】 | 无 usage【文档】 |
| 流式 `include_usage:true` | usage 在**单独的最后一个 chunk，`choices: []`**，之后是 `[DONE]`【实测】 | usage 在**最后一个 chunk 上，且该 chunk 同时带 `finish_reason` 和 1 个 choice**（delta.content 为 ""）；没有单独的 `choices:[]` chunk；其余 chunk `usage:null`；之后是 `[DONE]`。21 个流式 200 响应全部如此【实测，与文档一致】 | 最后单独 chunk，`choices: []`【文档】 | 单独末尾 chunk；另有 `continuous_usage_stats`（每个 chunk 都带累计 usage）【文档】 |
| `[DONE]` 结束标记 | 有【实测】 | 有【实测】 | 有【文档】 | 有【文档】 |
| delta 里的多余字段 | 无 | 思考模式：首个 chunk `{content:null, reasoning_content:"", role}`，先连续出 reasoning_content chunk（content 为 null），再出 content chunk（reasoning_content 为 null）；**默认开启思考**（不传 `thinking` 时返回 reasoning_content）【实测】 | 出现 `function_call:null`、`tool_calls:null`；`system_fingerprint` 为 null 或 ""【文档】 | 视版本可能有 `reasoning_content`（reasoning parser）【文档】 |
| 工具调用（非流式） | 英文提示：`tool_calls[0]` 带 `id=call_xxx`、`type=function`、`arguments` 为 JSON **字符串** `{"id":"T-1"}`，`finish_reason=tool_calls`【实测】；中文提示下 1.5B 模型直接用文字回答，没有调用工具【实测，属模型能力问题】 | 支持；`id` 形如 `call_00_xxx`，并行调用时 `call_01_`、`call_02_`；非思考模式下 content 常带一句前导文字（"I'll check…"），**content 与 tool_calls 同时出现**；函数名必须匹配 `^[a-zA-Z0-9_-]+$`，否则 400【实测】；strict（beta）【文档，未测】 | 支持；函数名 ≤64 字符【文档】 | **必须启动参数 `--enable-auto-tool-choice --tool-call-parser <parser>`**（Qwen2.5 用 `hermes`，DeepSeek-V3 用 `deepseek_v3`）；否则 auto 工具调用不可用【文档】 |
| `tool_choice:"required"` | **被忽略**：中文提示下仍返回纯文本，`finish_reason=stop`，没有报错【实测】 | 非思考：`none` 生效、`required` 和指定函数都**真的强制调用**（闲聊提示也会调，甚至编造参数如日期 2025-06-15）【实测】；思考模式：`required` 和指定函数都 **400** `Thinking mode does not support this tool_choice`【实测，与文档一致】；`none` 正常 | 【待测】 | `vllm>=0.8.3` 支持，靠结构化输出保证至少一次调用；首次调用有 FSM 编译延迟【文档】 |
| 流式工具调用 | 整个 tool_call（id、name、**完整 arguments**）在**一个 delta chunk** 里一次给出，不是增量【实测】 | **增量**：首个 tool delta 带 id+name、arguments 为 ""，之后每个 chunk 只有 `index`+arguments 片段（逐 token，如 `{"`、`id`、`"`、`: "`、`T`…）；并行调用按 index 0→1→2 **顺序**出，不交错；思考模式下 reasoning chunk 全部结束后才出 tool delta【实测，与文档一致】 | 表格写着「tools 不能与 stream=True 同时使用」——**疑似文档过时**【待测】 | 工具调用区域解析完成后**整段输出**（按 parser 而定）【文档】 |
| finish_reason 取值 | `stop` / `tool_calls`（另见 `length`）【实测前两项】 | 实测见到 `stop`、`tool_calls`；`insufficient_system_resource`、`aborted`【文档，未见】 | 标准值【文档】 | 标准值【文档】 |
| TTFT / 时延（本机 CPU，1.5B） | 流式 TTFT 34–202 ms（首次冷启动 202 ms，热态 34–70 ms）；短回复总时长 329–801 ms【实测】 | 从美西（CloudFront SFO）测：flash 非思考 TTFT 540–1109 ms，总 0.8–1.2 s；flash 思考 首 delta 580–923 ms，**首个正文 content 1.18–1.50 s**；pro 非思考 TTFT 950 ms/总 1.77 s，pro 思考工具 1.23 s/1.89 s；非流式 0.75–2.05 s【实测，单次样本少，仅供量级参考】 | — | — |
| 错误格式 | — | 统一 `{"error":{"code","message","param":null,"type"}}`，message 末尾带 `(request_id: …)`。无效模型 400（message 列出支持的模型名）；无效 key **401** `type=authentication_error`，message 回显 key 的末 4 位（`****<末4位，已脱敏>`）；max_tokens 超限 400（有效区间 [1, 393216]）；超长上下文 400 `maximum context length is 1048576 tokens… you requested 1300021 tokens`（服务端先 tokenize，耗时 3.4 s，未计费推理）【实测】 | 【待测】 | 【待测】 |
| 限流响应头 | — | **没有任何 `x-ratelimit-*` / `retry-after` 头**；只有 `X-Ds-Trace-Id`、CloudFront 头。错误响应 `X-Cache: Error from cloudfront`【实测】；429 未触发 | 【待测】 | 【待测】 |
| 思考模式 + 多轮工具 | — | 文档称带 tools 时必须回传历史 `reasoning_content`，否则 400；⚠️**实测不回传也返回 200**，结果正常（flash，2 轮）。思考模式下 `temperature` 等参数被忽略【文档，未测】；第 2 轮（读工具结果后）`reasoning_tokens` 常为 0【实测】 | — | — |


### 2.1 DeepSeek：实测 vs 旧结论（文档）差异

| 项目 | 旧结论 | 实测 | 状态 |
|---|---|---|---|
| 模型名 | deepseek-chat / deepseek-reasoner | 正式名 deepseek-flash / deepseek-v4-pro，旧名映射到 flash | ⚠️变化 |
| 不带 stream_options 时的 usage | 无 | **有**（末 chunk） | ⚠️不同 |
| include_usage 的 usage 位置 | 最后一个内容 chunk | 最后一个 chunk（带 finish_reason，delta 为空串） | ✅一致 |
| 流式工具参数 | 增量 | 逐 token 增量，并行调用按 index 顺序 | ✅一致 |
| 思考模式 required / 指定函数 | 400 | 400 | ✅一致 |
| reasoning_content 回传 | 旧报告写"不能回填" | 新文档要求带 tools 时必须回填；实测回填和不回填都返回 200 | ⚠️旧结论错误，已更正 |
| 思考模式默认 | 未记录 | 默认开启 | ⚠️新增 |
| 限流头 | 未记录 | 无 | 新增 |

运行：`bash -lc 'cd model-compat && go build -o /tmp/dsbench ./cmd/deepseek && /tmp/dsbench -out deepseek-runs'`（key 只通过 `os.Getenv("DEEPSEEK_API_KEY")` 读取）。
## 3. 对网关 / 适配层（D-11）的结论

1. **usage 解析不能只看"末尾 `choices:[]` 的 chunk"**：DeepSeek 把 usage 放在最后一个带 finish_reason 的 chunk 上（已实测）。正确做法：**每个 chunk 只要 `usage != null` 就记录，取最后一个非空值**；流结束仍没有 usage 时，计量要标记 `usage_missing` 并按本地 tokenizer 估算（同时告警）。
2. **请求一律带 `stream_options:{include_usage:true}`**，否则四家在流式下都拿不到 usage（Ollama 已实测）。
3. **tool_call 流式聚合器按 `index` 累加 `arguments` 字符串**，同时兼容"整段一次到达"（Ollama、vLLM）和"增量到达"（DeepSeek，已实测逐 token）；必须等 `finish_reason=tool_calls` 或流结束后再做 JSON 校验。
4. **`tool_choice:"required"` 不可靠**（Ollama 静默忽略，DeepSeek 思考模式直接 400，已实测；DeepSeek 非思考模式下有效）：平台不能依赖它来强制调用工具，应在服务端校验"本应调用工具却没调用"的情况，并重试或降级。
5. **未知字段一律忽略**（`reasoning_content`、`function_call:null`、`prompt_cache_*` 等）；~~`reasoning_content` 不能回填~~ **更正**：当前 DeepSeek 文档要求带 tools 时**必须回填**历史 reasoning_content；实测回填与不回填均 200。网关应**按文档回填**（保存 assistant 消息原样），不依赖当前的宽松行为。
6. DashScope 需要在配置里写明 **地域 + Workspace**；Key 与地域绑定，401 时应提示"地域不匹配"，而不是只报"Key 无效"。
7. vLLM 是否支持工具调用取决于**部署参数**：连接器的"连通性测试"里应带一次工具调用探测，不支持时在 UI 上标出该能力缺失。

## 3.1 网关适配要点（M1 checklist，DeepSeek 实测后）

- [ ] 模型名：配置只用 `deepseek-flash` / `deepseek-v4-pro`；旧名会被静默映射且**旧 `deepseek-reasoner` 会开思考**；用 `GET /models` 做连通性测试与能力发现（context_window、max_output_tokens、effort 等级、input_modalities）。
- [ ] **显式传 `thinking:{type:enabled|disabled}`**——默认是开启，不传会多出 reasoning token 费用和 ~0.5–0.9 s 首正文延迟。
- [ ] 流式 usage：每个 chunk 检查 `usage!=null`，取最后一个；不要假设有 `choices:[]` chunk；不要假设没 include_usage 就没有 usage（DeepSeek 总会给）。仍统一发送 `stream_options.include_usage=true`。
- [ ] 计量：缓存命中读 `prompt_cache_hit_tokens`（或标准 `prompt_tokens_details.cached_tokens`），思考 token 读 `completion_tokens_details.reasoning_tokens`，按命中/未命中/输出三档计价。
- [ ] SSE 解析：同时处理 `content:null` 和 `content:""`；reasoning_content 单独通道，默认不展示给终端用户、不写入审计正文（可存摘要）。
- [ ] 工具流聚合：按 `index` 拼接 arguments，id/name 只在首片；`finish_reason=tool_calls` 后再 JSON 校验；content 与 tool_calls 可能同时存在，两者都要保留。
- [ ] 函数名在网关侧预校验 `^[a-zA-Z0-9_-]+$`（MCP 工具名含 `.`、`/` 时需映射）。
- [ ] tool_choice：思考模式下 `required`/指定函数 → 网关自动降级（改为非思考，或改 auto + 服务端校验），不要把 400 抛给用户；非思考模式下 required 会让模型编造参数，业务侧需校验参数。
- [ ] 多轮工具：assistant 消息**原样保存并回传**（含 reasoning_content、tool_calls、content），即使当前实测不回传也 200。
- [ ] 错误映射：解析 `error.type`（`authentication_error`→凭据失效，`invalid_request_error`→按 message 关键字细分：模型名 / max_tokens / context length / tool_choice / 函数名），保留 `request_id` 与响应头 `X-Ds-Trace-Id` 便于工单。
- [ ] 401 message 会回显 key 末 4 位：网关日志里对错误 message 也做脱敏。
- [ ] 限流：无 ratelimit 头，只能靠 429/503 + 指数退避 + 本地令牌桶；`max_tokens` 上限 393216，超长上下文在网关侧先估算 token 拦截（服务端拒绝要 3 s+ 且上传 MB 级请求体）。
- [ ] 超时：TTFT 正常 0.5–1.3 s（美西测），首 token 超时建议 ≥10 s，思考模式按 effort 放宽；空闲 chunk 间隔超时单独设置。

## 4. 待办（需要 key / GPU）

- ~~DeepSeek~~（已完成）、DashScope、vLLM 各跑一遍 `main.go`，把【文档】列改成【实测】，重点核对：DashScope 流式工具调用、DashScope 缓存 token 字段、DeepSeek usage 实际所在 chunk。
- 用 7B 以上的模型复测中文工具调用和 required 语义。

## 5. 依赖与许可

- `main.go` 只用 Go 标准库。
- 测试用 Ollama（MIT）作为外部进程，不链接进代码；模型 qwen2.5:1.5b（Apache-2.0）只用于测试。

## 6. 文档来源（2026-11-02 抓取）

- DeepSeek API 文档：https://api-docs.deepseek.com/ （chat completion、thinking mode、function calling、usage 各页）
- 阿里云百炼 OpenAI 兼容：https://help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope
- vLLM 工具调用：https://github.com/vllm-project/vllm/blob/main/docs/features/tool_calling.md （官方站点抓取返回 429，改读 GitHub 源文件）
