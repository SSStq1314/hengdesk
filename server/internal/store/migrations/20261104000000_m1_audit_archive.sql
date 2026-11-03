-- FR-40 审计归档（D-34 第 4 点）：audit_logs 仍只追加，唯一例外是"受控归档删除"。
-- 只有当本事务设置了 hengdesk.audit_archive_checkpoint = <断点记录 ID>，且该断点是链上的
-- audit.archive.checkpoint 记录、被删记录的 ID 在断点登记的区间内（<= detail.last_id）并早于断点本身时，DELETE 才放行。
-- UPDATE、TRUNCATE、其他 DELETE 一律拒绝。断点本身在哈希链上，任何归档删除都有链上记录。
CREATE OR REPLACE FUNCTION hengdesk_audit_immutable() RETURNS trigger AS $$
DECLARE
  cp text;
  lim bigint;
BEGIN
  IF TG_OP = 'DELETE' THEN
    cp := current_setting('hengdesk.audit_archive_checkpoint', true);
    IF cp IS NOT NULL AND cp ~ '^[0-9]+$' THEN
      SELECT (detail->>'last_id')::bigint INTO lim FROM audit_logs
        WHERE id = cp::bigint AND action = 'audit.archive.checkpoint' AND hash <> '';
      IF lim IS NOT NULL AND OLD.id <= lim AND OLD.id < cp::bigint THEN
        RETURN OLD;
      END IF;
    END IF;
  END IF;
  RAISE EXCEPTION 'audit_logs is append-only (% rejected)', TG_OP;
END;
$$ LANGUAGE plpgsql;
