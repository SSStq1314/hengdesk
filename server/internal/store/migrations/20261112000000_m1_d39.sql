-- M1 第二块收尾（D-39）。
-- 第 1 条：手工白名单条目也可能需要复核（#31 把空端口手工条目改成 *，本次收窄为 443 / 上游实际端口并标「待复核」）。
-- 数据修正需要读取 #31 的 egress.rule.ports_migrate 审计并逐条写审计，由启动时的 egress.MigrateD39Ports 执行
-- （仅一次，标记 egress.d39_ports），不在 SQL 里做。
-- Modify "egress_rules" table
ALTER TABLE "egress_rules" ADD COLUMN "review_required" boolean NOT NULL DEFAULT false;
-- 第 7 条：切换到备用模型时记录原模型。
-- Modify "usage_records" table
ALTER TABLE "usage_records" ADD COLUMN "fallback_from" character varying NOT NULL DEFAULT '';
