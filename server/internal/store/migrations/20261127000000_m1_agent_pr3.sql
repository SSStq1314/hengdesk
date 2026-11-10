-- Modify "usage_records" table
ALTER TABLE "usage_records" ADD COLUMN "agent_id" bigint NULL;
-- Create index "usagerecord_agent_id_created_at" to table: "usage_records"
CREATE INDEX "usagerecord_agent_id_created_at" ON "usage_records" ("agent_id", "created_at");
