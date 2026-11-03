# 模型网关（M1 第二块 PR1 / PR2）

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

server / worker 启动时检查主密钥能否解密库内全部上游 Key；缺失或不匹配时直接启动失败并给出指引
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

## Ollama（本机验证步骤）

本开发机未安装 Ollama，PR1 未实测。验证步骤：

1. `ollama pull qwen3:8b && ollama serve`；
2. 创建供应商 `{"name":"ollama","dialect":"generic","base_url":"http://127.0.0.1:11434/v1","api_key":"ollama"}`；
3. 创建模型 `{"name":"local","provider_id":<id>,"upstream_model":"qwen3:8b"}`，按上面 curl 调用。
已知差异（见 docs/spikes/model-compat.md）：Ollama 忽略 `required`，usage 在单独的 `choices:[]` 块中，网关均能处理。
