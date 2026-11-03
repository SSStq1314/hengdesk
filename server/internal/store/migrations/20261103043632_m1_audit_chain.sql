-- Modify "audit_logs" table
ALTER TABLE "audit_logs" ADD COLUMN "prev_hash" character varying NOT NULL DEFAULT '', ADD COLUMN "hash" character varying NOT NULL DEFAULT '';
-- Create index "auditlog_prev_hash" to table: "audit_logs"
CREATE UNIQUE INDEX "auditlog_prev_hash" ON "audit_logs" ("prev_hash") WHERE ((hash)::text <> ''::text);
