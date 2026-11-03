# M1 第二块：模型网关、预算与配额、出站控制

## PR 拆分

| PR | 内容 | 状态 |
|----|------|------|
| PR1 模型网关核心 | OpenAI 兼容适配器（流式 / 非流式、tools、function calling）；DeepSeek 方言（思考模式 tool_choice 降级或拒绝、末块 usage、reasoning_content 回传、401 脱敏）；模型与上游 Admin CRUD（AES-256-GCM 加密 Key，主密钥来自安装时生成的文件 / 环境变量，API 永不返回明文）；每次调用 fail-closed 审计；统一内部 Chat API（SSE）；模拟上游 + 测试 | PR #25 已合并 |
| PR2 模型管理与用量 | 模型 ACL 管理界面、默认模型、价格表、usage_record 落库与查询、“测试连接”按钮、Admin 前端页面；D-36 主密钥；D-35 DeepSeek 契约测试 | 本 PR |
| PR3 预算与配额 | 组织 / 部门 / 用户预算，Valkey 计数与预扣、超额拦截、阈值告警 | 待做 |
| PR4 出站控制 | 出站域名白名单、出站日志（AC-09）、网关 HTTP 客户端统一走出站控制 | 待做 |
| PR5 适配器扩展 | Anthropic 适配器、Ollama / 本地模型方言、重试与故障转移 | 待做 |

## PR1 说明

- 没有采用 go-openai（偏离 D-24 原设想）：需要透传 DeepSeek 的 `reasoning_content`、`thinking` 字段，
  并自行控制 SSE 解析、超时与错误脱敏，因此用标准库实现了一个精简客户端（`server/internal/gateway`），
  未复制任何第三方代码。
- 模型使用权限：超级管理员（`model.manage`）可用全部模型；其他用户需在 ACL 中对 `model/<模型名>` 拥有 use 位。
  无权限、模型不存在或已停用一律 404。管理界面放到 PR2。
- 审计：调用上游前先写 `model.call` 尝试记录，写失败则拒绝调用（503，不触达上游）；
  调用结束写结果记录（耗时、usage、错误码、warning），只记录元数据，不记录对话内容。
- 详细用法见 `docs/ops/model-gateway.md`。

## PR1 验证

- CI：仅使用模拟上游（`gateway/mockupstream`，复现 DeepSeek 行为）。
- 本机真实 DeepSeek 冒烟（deepseek-flash，思考模式）：**通过**。流式 + 工具调用（required 被降级为 auto 并给出 warning），
  返回 1 个工具调用、带 reasoning_content、usage 来自上游；回传 reasoning_content 的第二轮正常结束；错误 Key 映射为
  `upstream_auth_failed` 且消息已脱敏。
- Ollama：本机未安装，未实测，步骤见 `docs/ops/model-gateway.md`。

## PR2 说明

- 管理后台新增「模型网关」页（需 `model.manage`，其他人接口 404、菜单不显示）：
  - 供应商：增删改，API Key 只写，「测试连接」请求上游 `/models`。
  - 模型：增删改、价格表（元 / 百万 token，库内存微元整数）、设为默认（未指定 model 的调用使用默认模型）、
    「测试连接」（发一条极短对话，用量记为 purpose=test）、使用权限（按成员 / 部门授权，部门可含下级，撤销即时生效）。
  - 用量：按模型 / 人 / 部门汇总调用次数、token、金额，可按日期过滤；最近调用明细。
- usage_record：每次调用（含失败）落一条，记录用户、调用时所在部门、模型、token（输入 / 缓存命中 / 输出 / 思考）、
  金额（按调用时价格算好落库，改价不影响历史）、耗时、错误码。上游没返回 usage 时由网关估算并标 `estimated=true`，
  汇总里单独给出估算的调用数、token 与金额，前端用「估算」标签标注。PR3 的预算与配额直接按此表的人 / 部门 / 模型维度聚合。
- 所有管理写操作 fail-closed 审计（写失败则拒绝执行）。
- D-36 主密钥：安装确认、后台常驻提示与确认、`hengdesk keys rotate-master` 事务内轮换、启动自检、备份不含主密钥，
  详见 [D-36](../decisions/D-36-master-key.md) 与 [model-gateway.md](../ops/model-gateway.md)。
- D-35：DeepSeek 录制回放契约测试（流式、思考、错误、超时）随 CI 运行。
  **通义 / vLLM 的契约测试移到 PR5**（适配器扩展）一起做。

## M1 验收清单（第二块相关）

- [ ] 通义（DashScope OpenAI 兼容）真实冒烟：流式、思考字段、工具调用、错误 Key 脱敏（D-35，未通过不算 M1 完成）
- [ ] vLLM 真实冒烟：同上（D-35，未通过不算 M1 完成）
- [x] DeepSeek 真实冒烟（PR1 已通过）
