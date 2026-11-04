-- Modify "conversations" table
ALTER TABLE "conversations" ADD COLUMN "tool_ids" jsonb NULL;
-- Modify "messages" table
ALTER TABLE "messages" ADD COLUMN "tool_calls" jsonb NULL, ADD COLUMN "tool_state" jsonb NULL;
