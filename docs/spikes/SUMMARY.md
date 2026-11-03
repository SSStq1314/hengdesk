# HengDesk（衡台）M0 技术 spike 总结

日期：2026-11-02（America/Los_Angeles）。各 spike 的详细结果见对应目录下的 REPORT.md。**凡是没有实测的内容，都已在报告中注明。**

## 交付方式
SSStq1314/hengdesk 主仓库目前只有 LICENSE 和 README，还没有 CI。按照要求，本次**只交付本地文件**，没有提 PR，也没有改动 /workspace/hengdesk。

## 各 spike 结论

| Spike | 结论 | 关键数据 |
|---|---|---|
| pdf-docx | 主解析器用 go-pdfium（WASM 模式，MIT；Go 1.25 下锁定 v1.19.8），回退用 ledongthuc/pdf 的 plain 模式；docx/pptx 用标准库解析 OOXML；xlsx 用 excelize | pdfium 抽取文本完整，表格行 4/4 保留；每页 1.5–3 ms（v1.21.4），锁定的 v1.19.8 约 2.7–7 ms；冷启动 2–2.8 s；二进制约 19–22 MB。ledongthuc 表格行 0/4 保留。截断的损坏文件只报错，不 panic |
| pgvector-acl | D-12 方向成立，但**必须分档执行**：候选集小时走精确检索，大时走 HNSW + iterative_scan=relaxed_order + ef≥100。**planner 默认行为不能用** | 30 万 × 768 维：1% 可见时默认计划召回率 0.037（平均只返回 0.4 条）；精确检索 p95 2.4 ms。10%/50% 可见时 ef100 relaxed 的 p95 为 3.6 / 2.1 ms，召回率 0.96。单个 KB 精确检索 p95 < 15 ms |
| mcp | mcp-go v1.5.0（MIT）可用；审批、审计、超时放在平台网关层，fail-closed | initialize 0.8–1.3 ms；审批超时或拒绝时执行 0 次，通过时恰好 1 次（AC-06）；300 ms 超时在 301 ms 触发；审计写失败时拒绝执行 |
| model-compat | 网关必须兼容两种 usage 位置和两种 tool_call 增量格式；不要依赖 `tool_choice=required` | 本地 Ollama（qwen2.5 1.5B，CPU）实测 TTFT 34–202 ms。DeepSeek **已实测**（51 次调用）：usage 在末 chunk、工具参数逐 token 增量、思考模式 required 400 与文档一致；不带 include_usage 也有 usage、旧版"不回填 reasoning_content"结论有误（已更正）；无限流头。M1 适配清单见 REPORT §3.1。DashScope、vLLM **未实测**，结论来自文档 |

## 阻塞与待决
1. **Go 1.25 版本锁定（2026-11-02 已实测，见 pdf-docx/REPORT.md 第 7 节）**：
   - **锁定版本**：go-pdfium **v1.19.8**；excelize **v2.11.1-0.20260915055537-22f76f9acb94**（go 1.26 升级前的最后一个提交）；x/net v0.58.0、x/crypto v0.55.0、x/text v0.41.0。用 go1.25.14 构建和 vet 均通过，代码零修改。pdfcpu 已排除。
   - **质量**：与新版逐字段**完全一致**，损坏文件的行为也一致。
   - **性能**：pdfium v1.19.8 每页约 2.7–7 ms，比 v1.21.4 慢约 2 倍。原因是库版本，与工具链无关。仍在 ≤20 ms/页的基线以内。
   - **govulncheck 可达漏洞**：只剩 1 个，excelize GO-2026-6685（超大行号导致 GetRows 死循环，属于 DoS）。修复需要 go 1.26，**这是无法消除的缺口**，必须用"带超时和资源上限的 worker + 行数上限"来缓解。
   - ⚠ **Go 1.25 已经不再接收安全补丁**（Go 1.27 已发布）。govulncheck 报出 11 项标准库漏洞（net/http、HTTP/2、crypto/tls、x509 等），修复版本都只有 1.26.9 以上。本模块调用不到这些漏洞，但平台的 HTTP 服务会受影响。**建议重新评估 D-24，改为 Go 1.26。**
2. DeepSeek 已实测；通义（DashScope）、vLLM 仍没有 key/GPU，需补测。
3. pgvector 需要在目标硬件上按 100 万 × 1024 维复测。
4. MCP 客户端超时后，服务端是否真的中止执行，未验证。有副作用的工具需要幂等键。

## 许可
没有复制任何 AGPL/GPL 代码。需要排除的库：go-fitz（AGPL）、unipdf（商业许可）。所用依赖都是宽松许可（MIT / Apache-2.0 / BSD），pgvector 扩展为 PostgreSQL License，明细见各 REPORT。

## PRD 性能基线建议（待在目标规模上复测）
- 向量检索（含 ACL，不含 embedding 和重排）：p95 ≤ 50 ms，recall@10 ≥ 0.95；低可见比例下必须返回满 k 条。
- 文档解析：PDF 每页 ≤ 20 ms（实测约 3 ms，留出余量）；解析服务常驻，避免每个文档都承受 2.8 s 冷启动。
- MCP 工具调用：网关额外开销 ≤ 5 ms；默认超时 30 s，可按工具配置；审批超时视为拒绝。
- 模型网关：TTFT 只记录不设硬门槛，取决于供应商；usage 统计覆盖率 100%（始终发送 include_usage）。
