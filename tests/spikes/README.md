# M0 技术验证（spike）代码

每个子目录是**独立 Go module**（工具链 go1.26.10），不属于 `server/` 主模块，不进主 CI。报告见 `docs/spikes/`。

| 目录 | 内容 | 运行 |
|---|---|---|
| `mcp/` | MCP 网关原型与 mock server | `go test ./...` |
| `model-compat/` | OpenAI 兼容模型接口探测（Ollama / DeepSeek） | `DEEPSEEK_API_KEY=... go run ./cmd/deepseek`（结果文件中 Authorization 与 401 回显的 key 末 4 位均已脱敏） |
| `pdf-docx/` | txt/md/docx/xlsx/pptx/PDF 解析评测 | 先生成样例，再 `go run ./cmd/bench` |
| `pgvector-acl/` | pgvector + ACL 检索压测（需 PostgreSQL 17 + pgvector） | 见 `docs/spikes/pgvector-acl.md` |

## 未入库的大文件
- PDF 样例（`pdf-docx/samples/*.pdf`、`pdf-docx/eval/*.pdf`，最大约 1.2 MB）不入库，用 `python3 pdf-docx/samples/gen_samples.py` 生成 Flat ODF 后由 LibreOffice 转换（`soffice --headless --convert-to pdf`），再复制到 `eval/`。
- 编译产物（`model-compat/model-compat`、`pgvector-acl/sqlx`、`pgvector-acl/pgbench-acl`）不入库。
