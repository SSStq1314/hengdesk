# 模型网关（M1 第二块 PR1 / PR2 / PR5）

## 主密钥

上游 API Key 用 AES-256-GCM 加密后存库（AAD 绑定供应商名称），主密钥为 32 字节（hex 或 base64）：

- `HENGDESK_MASTER_KEY_FILE`：主密钥文件路径（优先）；
- `HENGDESK_MASTER_KEY`：直接给出主密钥。

`install.sh` 在 `deploy/secrets/master.key` 不存在时生成（权限 600，`--force` 重装不会覆盖），并写入 `.env`；
`upgrade.sh` 会为旧安装补上。**主密钥丢失后已保存的上游 Key 无法解密，只能重新录入**，请与数据库备份分开保管。
未配置主密钥时可以管理模型，但保存 Key 返回 503，调用返回 `upstream_key_unavailable`。

### 安装确认与后台提示（D-36）

- `install.sh` 结束时打印主密钥路径和「丢失后已存的 Key 无法恢复，请与数据库备份分开保管」，交互模式须输入 `yes`；
  非交互 / 自动化安装须显式带 `--ack-master-key`，否则安装失败。
- 管理后台在管理员确认前常驻红色提示（显示路径），点「我已单独备份」后消失（记审计 `system.master_key.ack`）。
  确认绑定主密钥指纹，轮换后需要重新确认。

### 启动自检

server 启动时检查主密钥能否解密库内全部上游 Key；缺失或不匹配时直接启动失败并给出指引
（恢复正确的 `secrets/master.key`，或在后台清除 Key 后重新录入）。单独检查：

```bash
cd deploy && docker compose --profile tools run --rm keytool keys check
```

### 轮换

```bash
cd deploy
docker compose stop server worker
docker compose --profile tools run --rm keytool keys rotate-master --yes
./upgrade.sh          # 或手动把 secrets/master.key 的新值写入 .env 的 HENGDESK_MASTER_KEY，再启动
```

- 在一个事务里用新密钥重新加密全部上游 Key 并写审计 `model.master_key.rotate`；任何一步失败整体回滚，密钥文件不动。
- 旧密钥保留为 `secrets/master.key.bak`，确认新密钥已备份、服务正常后由管理员自行删除。
- 轮换后后台会重新要求确认备份。

### 备份

`/backups`（`hengdesk backup` / `upgrade.sh` 的升级前备份）**不包含主密钥**，备份完成时日志提示单独保管。
恢复时需要同时有数据库备份和当时的主密钥。

## Admin API（仅超级管理员，其他人 404）

- `GET/POST /api/v1/admin/model-providers`，`PATCH/DELETE /api/v1/admin/model-providers/:id`
  字段：`name`、`dialect`（`generic` / `deepseek`）、`base_url`、`api_key`（只写）、`clear_api_key`、`enabled`、`timeout_seconds`。
  响应只有 `has_api_key`，永远不返回 Key。仍有模型引用时删除返回 409。
- `GET/POST /api/v1/admin/models`，`PATCH/DELETE /api/v1/admin/models/:id`
  字段：`name`（对内模型名）、`provider_id`、`upstream_model`、`thinking`（`off` / `on` / `provider_default`）、
  `tool_choice_policy`（`downgrade` / `reject`）、`context_window`、`max_output_tokens`、`enabled`。

## 模型管理与用量（PR2，仅 `model.manage`，其他人 404）

- `POST /admin/model-providers/:id/test`：测试连接（请求上游 `/models`，错误已脱敏）。
- `POST /admin/models/:id/default`：设为默认模型；`POST /admin/models/:id/test`：发一条极短对话（用量记为 `purpose=test`）。
- 价格字段：`price_input_micros`（未命中缓存输入）、`price_cached_micros`（缓存命中输入）、`price_output_micros`（输出，含思考），
  单位为每百万 token 的「微元」（1 元 = 1,000,000）。金额 = Σ(token × 单价) / 1e6，四舍五入到微元，在调用结束时算好落库。
- `GET/POST /admin/models/:id/acl`，`DELETE /admin/models/:id/acl/:type/:sid`：`subject_type` 为 `user` / `dept` / `role`，
  部门可 `include_children`。
- `GET /admin/usage/summary?group_by=model|user|department|total&from=YYYY-MM-DD&to=YYYY-MM-DD`：
  汇总 `calls`、`error_calls`、各类 token、`cost_micros`，以及 `estimated_calls` / `estimated_tokens` / `estimated_cost_micros`
  （上游未返回 usage、由网关估算的部分）。
- `GET /admin/usage/records`：最近 200 条明细，`estimated=true` 表示估算。`GET /usage/me`：本人按模型的用量。
- 管理后台：左侧「AI → 模型网关」。

## Chat API

`POST /api/v1/chat/completions`（登录会话 + CSRF），请求体与 OpenAI 兼容：`model`、`messages`、`tools`、`tool_choice`、
`temperature`、`max_tokens`、`stream`。

- 非流式：返回 `{message, finish_reason, usage, warnings}`；
- 流式：SSE，事件 `delta`（增量文本 / reasoning / 工具参数）、`done`（完整 assistant 消息 + usage，可直接放回下一轮 messages）、`error`；
- DeepSeek 思考模式下 `tool_choice=required` 或指定函数：`downgrade` 策略降为 `auto` 并在 `warnings` 中说明，`reject` 策略返回 400 `tool_choice_unsupported`；
- 多轮工具调用：把上一轮返回的 assistant 消息（含 `reasoning_content`）原样放回即可；
- 上游错误统一为 `{"error":{"code","message"}}`，消息中的 Key 片段已脱敏。

## 本地演示（模拟上游，无需真实 Key）

```bash
cd server && go run ./cmd/mockupstream -addr 127.0.0.1:18080 -key sk-demo   # 终端 1
# 终端 2：启动服务时设置 HENGDESK_MASTER_KEY=$(openssl rand -hex 32)，用管理员登录后：
curl -b c.txt -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"name":"deepseek","dialect":"deepseek","base_url":"http://127.0.0.1:18080","api_key":"sk-demo"}' \
  http://localhost:8080/api/v1/admin/model-providers
curl -b c.txt -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"name":"ds-flash","provider_id":1,"upstream_model":"deepseek-flash","thinking":"on"}' \
  http://localhost:8080/api/v1/admin/models
curl -N -b c.txt -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"model":"ds-flash","stream":true,"messages":[{"role":"user","content":"查工单 T-1"}],"tools":[{"type":"function","function":{"name":"get_ticket","parameters":{"type":"object"}}}]}' \
  http://localhost:8080/api/v1/chat/completions
```

接真实 DeepSeek 时把 `base_url` 换成 `https://api.deepseek.com`、`upstream_model` 用 `deepseek-flash` / `deepseek-v4-pro`。

## 供应商协议与方言（PR5）

| 协议 `kind` | 方言 `dialect` | 适用 | 思考开关的发送方式 |
|---|---|---|---|
| `openai_compat` | `deepseek` | DeepSeek | `thinking: {type: enabled/disabled}`，回传 `reasoning_content` |
| `openai_compat` | `dashscope` | 通义（`https://dashscope.aliyuncs.com/compatible-mode/v1`） | `enable_thinking: true/false` |
| `openai_compat` | `vllm` | vLLM OpenAI 兼容服务（`http://<host>:8000/v1`） | `chat_template_kwargs.enable_thinking`；同时识别 `reasoning` / `reasoning_content` |
| `openai_compat` | `generic` | 其他 OpenAI 兼容，含 Ollama | 不发送 |
| `anthropic` | （忽略） | Anthropic Messages API（`https://api.anthropic.com`） | `thinking: {type: enabled, budget_tokens}` |

**内网模型必须显式加白名单。** vLLM / Ollama 通常部署在内网，而出站控制默认拦截内网地址（PR4）。
添加供应商前，先由超级管理员在「出站控制 → 出站白名单」放行对应网段和端口，例如 `cidr 10.20.0.0/16`、端口 `8000`（vLLM）或 `11434`（Ollama）。
管理界面在选择 vLLM / 通用方言时会给出同样提示。

### Anthropic 适配器

- 走统一出站客户端；请求头 `x-api-key` + `anthropic-version: 2023-06-01`；`base_url` 未带 `/v1` 时自动补上。
- 请求转换：`system` 消息合并为顶层 `system`；`tool` 消息转为 `tool_result` 块；相邻同角色消息合并；
  `tools` → `input_schema`；`tool_choice`：`auto`→`auto`、`none`→`none`、`required`→`any`、指定函数→`tool`；`stop` → `stop_sequences`。
- `max_tokens` 必填，未指定时用 4096。开启思考时 `budget_tokens = max_tokens / 2`（须 > 1024，否则 400），并去掉 `temperature` / `top_p`（返回告警）；
  思考模式下强制工具选择按模型的「工具选择策略」降级为 `auto` 或拒绝。
- 响应转换：thinking 块 → `reasoning_content`，签名保存在 `reasoning_signature`，多轮工具调用时须原样回传（无签名的思考内容不会发回上游）。
  `stop_reason`：`end_turn`→`stop`、`max_tokens`→`length`、`tool_use`→`tool_calls`、`refusal`→`content_filter`。
- 用量：`prompt_tokens = input_tokens + cache_read_input_tokens + cache_creation_input_tokens`，`cached_tokens = cache_read_input_tokens`。
- 错误消息与其他上游一样脱敏（不回显 Key）；529 overloaded 视为可重试。

## 重试与故障转移（PR5）

- **只在首字节之前重试**，且只针对：建立连接阶段的失败（连接超时、连接被拒，502）、429、5xx（含 Anthropic 529）。4xx（参数、鉴权）、读超时（504）和连接建立后的中断不重试（D-39 第 5 条）。
- **流式收到首个 token 后不再重试或切换**（含 Anthropic 流内 `error` 事件），直接以 SSE `error` 事件结束；首个 token 之前的失败按非流式规则处理（D-39 第 8 条）。
- 每个上游最多尝试 2 次（间隔 200 ms × 次数），用尽后按模型配置的**备用模型**（`fallback_model_ids`，最多 3 个，按顺序）依次切换。
  备用模型须存在、已启用、不能是自身、不能重复；管理端在「模型 → 编辑 → 备用模型」勾选。
- **每次尝试都单独写审计**（`model.call` / `attempt`，含 `attempt` 序号，切换时带 `fallback_model`）和 `usage_record`（`attempt` 字段，同一 `request_id`）。
  第 1 次尝试的审计写失败 → 拒绝请求；后续尝试的审计写失败 → 停止重试，返回上一次错误（不会在无审计的情况下调用上游）。
- **预算只计实际产生的用量一次**：预算预检只在第一次尝试前做一次（以主模型为准）；失败尝试 token 与金额为 0（标记用量未知，D-37），
  只有成功那次按实际用量记账。
- **只切换到调用人有使用权限的备用模型**（D-39 第 6 条）：逐个按资源 ACL 使用位校验；都没有权限时返回主模型的原错误，不调用备用。
  模型列表 / 详情返回 `fallback_acl_mismatch`，编辑框在使用人群与主模型不一致的备用旁提示。
- **记录实际使用的模型**（D-39 第 7 条）：响应 `model` 为实际模型并带 `fallback_from`；`usage_record.fallback_from`；审计 `resource_id` 为实际模型，`detail.note`「由 X 切换」。

## 契约测试与录制（D-35）

- 回放带在 `server/internal/gateway/testdata/`：`deepseek_cassette.json`（真实录制）、`dashscope_cassette.json`、`vllm_cassette.json`、`anthropic_cassette.json`。
  `dashscope_cassette.json` 按 D-49 只收录通义官方文档示例（每条有来源 URL、获取时间和非语义变换），**不是真实调用**。vLLM / Anthropic 仍由 `tests/spikes/model-compat/make-doc-cassettes.py` 构造，文件头标明「非真实录制、非通义实测」；该脚本不得覆盖 dashscope 回放带。
- 测试（`contract_vendors_test.go`）覆盖：非流式 / 流式、思考字段开关、错误（无效 Key、无效模型、超长上下文、Anthropic overloaded）、超时；CI 离线运行。
- 拿到凭据后在**本机**真实录制（不要在 CI 运行，不要使用 `DEEPSEEK_API_KEY`）：

```bash
cd server
HENGDESK_RECORD_API_KEY=<通义 Key> go run ./cmd/cassette-record -vendor dashscope \
  -base https://dashscope.aliyuncs.com/compatible-mode/v1 -model qwen-plus \
  -out internal/gateway/testdata/dashscope_cassette.json
# vLLM（内网，无 Key 可不设；需显式放行内网地址）
go run ./cmd/cassette-record -vendor vllm -base http://10.20.0.5:8000/v1 -model Qwen/Qwen3-8B \
  -allow "cidr 10.20.0.0/16 8000" -out internal/gateway/testdata/vllm_cassette.json
HENGDESK_RECORD_API_KEY=<Anthropic Key> go run ./cmd/cassette-record -vendor anthropic \
  -base https://api.anthropic.com -model claude-sonnet-4-5 -out internal/gateway/testdata/anthropic_cassette.json
go test ./internal/gateway/ -run Contract
```

  录制工具只保存请求体与响应体（不保存任何请求头），写盘前把 Key 替换为 `[REDACTED]` 并断言文件中不含 Key；同样经出站客户端，默认只放行 `-base` 的主机，出站日志写到 `-egress-log`（默认 `cassette-record-egress.jsonl`，勿提交）。
  录制后检查 diff，确认无敏感内容再提交。

## Ollama（本机验证步骤）

本开发机未安装 Ollama，PR1 / PR5 均未实测。验证步骤：

1. `ollama pull qwen3:8b && ollama serve`（默认监听 `127.0.0.1:11434`；服务端与 Ollama 不在同一主机时设 `OLLAMA_HOST=0.0.0.0`）；
2. 在「出站控制 → 出站白名单」放行 Ollama 地址，如 `ip 127.0.0.1` 端口 `11434` 或 `cidr 10.20.0.0/16` 端口 `11434`（内网地址默认拦截）；
3. 创建供应商 `{"name":"ollama","dialect":"generic","base_url":"http://127.0.0.1:11434/v1","api_key":"ollama"}`；
4. 在管理界面点「测试连接」，应列出 `qwen3:8b`；
5. 创建模型 `{"name":"local","provider_id":<id>,"upstream_model":"qwen3:8b","thinking":"provider_default"}`，按上面 curl 分别调用非流式、流式、带 `tools` 的请求；
6. 在「用量」页确认有 usage_record，在审计日志确认有 `model.call` 记录。
已知差异（见 docs/spikes/model-compat.md）：Ollama 忽略 `required`，usage 在单独的 `choices:[]` 块中，网关均能处理。
