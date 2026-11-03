-- M1 第二块 PR4b（D-38）：白名单条目复核标记。升级 / OIDC 自动添加的已有条目 reviewed_at 为空，即「待复核」。
-- 端口语义（空 = 仅 443，* = 任意端口）的数据修正需要按模型上游 URL 计算实际端口并逐条写审计，
-- 由启动时的 egress.MigratePorts 执行（仅一次，标记 egress.d38_ports），不在 SQL 里做。
-- Modify "egress_rules" table
ALTER TABLE "egress_rules" ADD COLUMN "reviewed_at" timestamptz NULL, ADD COLUMN "reviewed_by" bigint NULL;
