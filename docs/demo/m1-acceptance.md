# M1 验收演示手册

给验收人用的操作说明：在一台 **x86_64、已安装 Docker Compose v2、建议 ≥20 GiB 可用磁盘** 的离线机器上，按 PRD §13 跑完 15 步，并对照每步预期。不必在本机编译代码。更完整的安装参数见 `docs/ops/offline-install.md`。

本手册的时间按 **America/Los_Angeles** 标注；应用界面默认时区是 Asia/Shanghai。

## 构建与库（D-48）

- 正式离线包必须用 **`bf4d437` 或更新的提交** 打包（`git rev-parse HEAD` 应等于该提交或在其后）。更早的构建不得用作 M1 发布包。
- **旧审计链不迁移**。演示库、测试库若曾用更早构建写过审计，请清空后重新初始化（例如 `uninstall.sh --yes --volumes` 再安装，或删掉数据卷后重装）。`secrets/` 默认会保留，如需全新主密钥需另行处理。
- 验收（本节 A 的安装记录与本节 B 的 15 步）必须在**新初始化的数据库**上做，不要复用修复前的库。
- 从本正式版起，审计哈希规范化不得静默改变；详见 `docs/dev/audit.md` 与 `docs/release/m1.md`。

## 准备两份包

在有网的构建机（不要把密钥写进仓库或 `.env` 提交），检出 `bf4d437` 或更新的提交后：

```bash
# 1) 默认正式包（AC-01 / D-46 干净安装记录用这个）
./deploy/pack-offline.sh --version <版本> --out dist
# 产物：dist/hengdesk-offline-<版本>-amd64.tar.gz

# 2) 验收包（15 步剧本，额外含 mocks）
./deploy/pack-offline.sh --version <版本> --out dist --with-acceptance
# 产物：dist/hengdesk-offline-<版本>-amd64-acceptance.tar.gz
```

把 tar 拷到目标机。目标机不要再 `docker pull` / `docker build`。

## A. 正式包干净安装（AC-01 / D-46）

这是 M1 验收必须留下记录的一项。真机界面演示曾因执行环境没有 Docker，改成二进制部署，**没有覆盖本项**；请在干净、断网的 x86 机器上重做并归档（见 `docs/demo/offline-clean-install.md`）。

```bash
tar -xzf hengdesk-offline-<版本>-amd64.tar.gz
cd stage-hengdesk-offline-<版本>-amd64/deploy   # 目录名以包内为准
./offline-check.sh
./install.sh --non-interactive --ack-master-key --offline --storage seaweedfs \
  --admin-password '<你的密码>' --up
```

预期：`curl http://<主机>:8080/healthz` 成功；浏览器打开后可用管理员邮箱（默认 `admin@hengdesk.local`）登录；安装与启动过程没有对外拉取镜像。

卸载（先备份）：`./uninstall.sh` 只删容器；加 `--volumes` 会删数据卷，必须交互确认或非交互加 `--yes`；`secrets/`（主密钥）始终保留。

## B. 用验收包跑 15 步脚本

默认正式包不含 mocks。请改用验收包解压安装（步骤同 A，版本号一致），然后：

```bash
cd deploy
../tests/acceptance/run.sh --compose --offline --version <版本>
```

脚本会对已安装实例发 HTTP 请求（cookie 会话 + CSRF），每步有断言，失败即停并打印步骤号和请求/响应摘要（不含密钥）。预期最后一行：

```
M1 端到端演示剧本 15 步全部通过。
```

也可以在已启动、已配置 OIDC 与 mocks 的环境里用界面逐步对照下面的「每步预期」。界面路径以中文为准。

### 每步预期

| 步 | 做什么 | 界面 / 脚本上应看到 | 对应 AC |
|---|---|---|---|
| 1 | 打开衡台，管理员登录，出站白名单加入 mock 端点 | 登录成功；未登录访问会话接口返回 **401**。出站默认拒绝，链路本地地址 `169.254.169.254` 不可放行 | AC-01（安装）、未登录 401 |
| 2 | 导入部门/人员 CSV，A/B/C/D/经理经 SSO 首次登录 | 自动建号并归入部门（CSV 列：`dept_path,name,email,employee_no`）。审计里能看到登录 | AC-02 |
| 3 | 配置「国产 API」与「本地」两个供应商（均指向 mockupstream），点「测试」 | 测试可流式对话；**任何角色查看配置都看不到密钥明文** | AC-14、AC-15 |
| 4 | 给财务部设月度 token 预算（演示用小值，默认 80 token；80% 告警，100% 拦截） | 保存成功 | AC-10（配置） |
| 5 | 建知识库，上传差旅/费用说明与「高管差旅标准」（含标记 `HD-EXEC-SECRET-991`） | 解析成功；高管文件只授权财务经理 | — |
| 6 | 登记 mock 工单 MCP，发现工具 | **新工具默认禁用**；将 `query_ticket` 设为自动、`create_ticket` 设为需审批（审批人 C） | — |
| 7 | 创建并发布「财务助手」（本地模型 + 知识库 + 两个工具），授权财务部 | 发布后市场部默认不可见 | — |
| 8 | A 问差旅标准，导出会话 | 回答带引用，**不含高管文件**；有 AIGC 角标、`X-HengDesk-AIGC`；复制带后缀「本内容由人工智能生成（衡台 HengDesk）」；Markdown 导出以 YAML front matter（`---`）开头；JSON 含标识元数据。非法文案（空或不含「人工智能生成 / AI 生成」）保存应 **400** | AC-04、AC-13、D-46 |
| 8 后 | 把高管文件只读权限授给 B，B 再问一次 | 无需重建索引即可检索到（AC-05） | AC-05 |
| 9 | A 要求创建报销工单 | 对话进入等待审批；通过前 mockmcp `create_ticket` 调用次数为 0。可选：此时重启 server（AC-08），会话仍在，C 通过后对话能继续。通过后该工具恰好执行 **1** 次 | AC-06、AC-08 |
| 10 | A 再要求建工单，C 拒绝 | 模型侧收到已拒绝，工具未执行，有审计 | AC-07 |
| 11 | 市场部 B 看智能体广场 / 打开财务助手详情 | 列表不可见，详情 / 开会话返回 **404**（不是 403） | AC-03 |
| 12 | 打开越界演示开关（白名单内端点 307 到外部域名或 `169.254.169.254`） | 出站被拦截并写入站日志；登记白名单外 MCP 失败。放行日志应写回状态码（缺陷 D3 修复前此处会失败） | AC-09 |
| 13 | A 连续提问直到预算用尽 | 80% 告警（经理通知 `budget_alert`），100% 拦截，错误文案可读（含「预算 / 用尽 / 管理员」一类说明）。界面若只闪现提示见缺陷 D6，脚本看 HTTP 状态与正文 | AC-10 |
| 14 | 审计员 D 按 `X-Trace-Id` 回放；尝试改配置；超管删审计；校验哈希链 | 回放能看到链路；D 改配置 **404**；删除审计接口不存在或被拒；`GET /api/v1/admin/audit-logs/verify` 为 `ok: true` | AC-11、AC-12 |
| 15 | 打开用量看板，对账财务部 token | 看板与汇总 / 明细一致，估算单独标注 | AC-16 |

导出他人会话（B 导出 A）应返回 **404**（D-46）。

## 失败时看哪里

1. 脚本终端：最后一步的步骤号、请求路径、响应摘要（不应出现密钥）。
2. 应用日志：`cd deploy && docker compose logs --tail 200 server worker`。
3. 出站日志：管理后台「出站控制」，或接口 `/api/v1/admin/egress/logs`。
4. 审计：管理后台「审计日志」，或 `/api/v1/admin/audit-logs` 与 `/audit-logs/traces/:id`。
5. CI：GitHub Actions job「断网 15 步验收」。该 job 还会用 iptables 拦截容器公网出站，内核日志前缀为 `HD_EGRESS`，出现即失败。

不要把 `DEEPSEEK_API_KEY` 或其他密钥写进仓库、CI 日志或提交的 `.env`。真实 DeepSeek 联调只在本机有环境变量时可选，只记录通过与否。通义真实冒烟按 D-47 **挂起，M2 W1 补测**；vLLM CPU 协议冒烟见 `docs/demo/vllm-cpu-smoke.md`（需出网，不进断网 CI）。

## 已知缺陷（M1 验收前必须关闭）

真机演示（2026-11-09 19:22–19:30 America/Los_Angeles）还发现 D1–D6，详见 `docs/progress/m1-block8.md`。脚本按正确行为断言，不放宽。其中 D5（哈希链）已随审计修复合入；D1、D2、D3、D4、D6 由独立缺陷修复工作区负责，合入前第 12 步（D3）或界面步骤可能失败。
