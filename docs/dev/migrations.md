# 数据库迁移开发规范（ent + Atlas 版本化迁移）

- 迁移文件放在 `server/internal/store/migrations/`，采用 Atlas 目录格式，`atlas.sum` 防止篡改和漏提交。文件编译时内嵌进二进制。
- **只向前**：只能新增迁移文件，不写 down，也不修改已合并的文件。migrate 会比对已执行文件的哈希，不一致即拒绝执行。
- 执行器（`store.ApplyMigrations`）的行为：
  - 用 PostgreSQL advisory lock 防止并发执行；
  - 每个文件在一个事务里执行；
  - 执行记录写入 `hengdesk_schema_revisions`。

## 修改 schema 的流程
```bash
cd server
# 1. 修改 internal/ent/schema/*.go
go generate ./internal/ent
# 2. 生成迁移（需要一个带 pgvector 的 PostgreSQL；工具会建临时库、用完删除）
go run ./cmd/migdiff -dev 'postgres://user:pass@localhost:5432/postgres?sslmode=disable' -name add_xxx
# 3. 手写迁移（触发器、函数等）：新建 <时间戳>_<名称>.sql，然后重算 atlas.sum
go run ./cmd/migdiff -hash
```
CI（go-postgres job）会执行 `go run ./cmd/migdiff -check`：ent schema 与迁移目录不一致，或 `atlas.sum` 校验失败，都会导致 CI 失败。

单元测试仍用 SQLite 和 `store.CreateSchemaForTest`（ent 自动建表）。PostgreSQL 上的迁移行为由 `HENGDESK_PG_IT=1` 的集成测试覆盖。

> 本项目使用 Atlas 的 Go 库（`ariga.io/atlas`，Apache-2.0）生成和校验迁移，不依赖 Atlas CLI 二进制。
