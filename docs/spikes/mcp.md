# Spike 3：mcp-go 模拟服务端 + 客户端，网关钩子 / 审计 fail-closed

日期：2026-11-02（America/Los_Angeles）　状态：**已实测**（`go test -v ./...`，输出见 `test-output.txt`）

## 1. 结论

- **mcp-go v1.5.0（MIT）可以直接用于 M0/M1**：Streamable HTTP 和 SSE 两种传输都跑通，协议版本协商为 `2026-07-28`。
- 审批、审计、超时都不依赖 mcp-go 内部的钩子，而是放在**平台自己的 `gateway.CallTool` 包装层**（D-11）。这一层是工具调用的**唯一出口**，执行顺序固定为：
  `策略(Policy) → 审批(Approver，带超时) → 预执行审计(必须写成功) → client.CallTool(带超时) → 结果审计`
  任何一步出错都**拒绝执行**（fail-closed），`Decision` 的零值就是 `Deny`。
- **不要信任工具自带的 annotations**：mock 中三个工具都没有设置 destructiveHint，mcp-go 却把它们都报成 `destructive=true`（未设置时规范默认按 true 处理）；反过来，恶意服务端也可以把危险工具标成只读。所以"是否需要审批"必须由**平台侧配置的策略**决定。

## 2. 实测结果（两种传输都是 PASS）

| 用例 | Streamable HTTP | SSE |
|---|---|---|
| initialize（本机回环） | 1.34 ms | 0.78 ms |
| tools/list | 3 个工具 | 3 个工具 |
| 正常调用 query_ticket | 返回 `T-1: 打印机故障` | 同左 |
| 业务错误 | `isError=true`，审计记为 tool-error | 同左 |
| 未知工具（绕过网关直连） | 协议错误 `invalid params: tool 'no_such_tool' not found` | 同左 |
| 未知工具（经过网关） | 策略层 deny，**没有发到服务端** | 同左 |
| 审批超时 | ErrApprovalTO，服务端执行 **0 次** | 同左 |
| 审批拒绝 | ErrRejected，服务端执行 **0 次** | 同左 |
| 审批通过 | 服务端 create_ticket 执行**恰好 1 次**（对应 AC-06） | 同左 |
| 调用超时（slow_op，超时设 300 ms） | 301 ms 触发 `context deadline exceeded`；之后同一个 client 仍然可用 | 同左 |
| 策略引擎报错 | ErrPolicyFailed，不执行 | 同左 |
| 审计写入失败 | ErrAuditFailed，不执行 | 同左 |
| 审计事件数 | 14 | 14 |

审计事件只记录参数的 sha256 哈希（`hashArgs`），不存明文，避免把敏感参数落进日志；是否需要另外加密存储明文，留给 PRD 决定。

## 3. 未测 / 风险

1. **客户端超时后服务端是否真的停止执行**：客户端 context 取消后，mcp-go 会断开请求，但服务端的 handler 是否收到 `notifications/cancelled` 并中止，**未验证**。对有副作用的工具，平台应要求**幂等键**（例如把 trace_id 作为参数传入），并在超时后把状态记为"未知"而不是"失败"。
2. stdio 传输、OAuth 授权、服务端主动发起的 sampling/elicitation 请求，均未测。
3. 并发与压测未做。

## 4. 代码结构

- `mock/mock.go`：模拟服务端，提供 `query_ticket`、`create_ticket`（带服务端执行计数器 `CreateCalls`）、`slow_op` 三个工具。
- `cmd/mockserver`：独立运行 `go run ./cmd/mockserver -addr :8931 [-sse]`。
- `gateway/gateway.go`：Policy / Approver / Auditor 接口 + fail-closed 的 `CallTool`。
- `spike_test.go`：上表所有用例；`go test -v ./... | tee test-output.txt`。

## 5. 依赖与许可

| 依赖 | 版本 | 许可 |
|---|---|---|
| github.com/mark3labs/mcp-go | v1.5.0 | MIT |
| 间接依赖（google/uuid、spf13/cast、yosida/jsonschema 等） | — | BSD-3 / MIT / Apache-2.0 |

没有参考或复制 AGPL/GPL 项目代码。
