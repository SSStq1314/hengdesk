-- Modify "knowledge_bases" table
ALTER TABLE "knowledge_bases" ADD COLUMN "strict_grounding" boolean NOT NULL DEFAULT false;
-- Modify "messages" table
ALTER TABLE "messages" ADD COLUMN "kb_ungrounded" boolean NOT NULL DEFAULT false;
