-- M1 第四块 PR3：会话选用的知识库、回复引用（FR-18）。
-- Modify "conversations" table
ALTER TABLE "conversations" ADD COLUMN "kb_ids" jsonb NULL;
-- Modify "messages" table
ALTER TABLE "messages" ADD COLUMN "citations" jsonb NULL;
