# D-31 数据库迁移与备份恢复

> 与 `ai-office-platform/decisions.md` 中 D-31 内容一致（同步副本）。
## D-31 数据库迁移：ent + Atlas 版本化、只向前、升级前自动备份（落实 D-30「迁移」）
- 决策日期：2026-11-02（工程）
- 拍板内容：
  - (a) 迁移文件采用 Atlas 目录格式，放在 `server/internal/store/migrations/`，以 `atlas.sum` 防篡改，编译时内嵌进二进制。生成与校验用 Atlas 的 Go 库（Apache-2.0），不依赖 Atlas CLI 二进制。
  - (b) 只向前执行，不写 down 迁移。已执行的文件按哈希校验，被改动即拒绝执行；数据库版本比程序新（降级运行）也拒绝。
  - (c) `hengdesk migrate` 发现待执行迁移且数据库非空时，先 `pg_dump -Fc` 备份到 `/backups` 卷并校验可读；备份失败则中止，数据库保持不变。
  - (d) 回滚即恢复：`deploy/restore.sh` 加 `docs/ops/backup-restore.md`。CI 的 compose 冒烟覆盖「备份 → 升级 → 恢复」全链路。
  - (e) CI 用 `migdiff -check` 校验 ent schema 与迁移目录一致。
  - (f) M0 预览版（ent 自动建表）的库不提供原地升级，migrate 检测到后报错退出。
- 理由：私有化部署无法由厂商代为回滚；down 迁移在真实数据上不可靠；备份即回滚点，最容易验证。
- 备选方案：
  - golang-migrate 加手写 up/down：要手写 SQL，与 ent schema 容易漂移，否决。
  - ent 自动建表（M0 方式）：无版本记录，不可审计，否决。
