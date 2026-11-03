-- M1 第二块 PR5：备用模型（故障转移）、用量记录的尝试序号。kind 增加 anthropic、dialect 增加 dashscope / vllm（varchar 枚举，无需改表）。
-- Modify "models" table
ALTER TABLE "models" ADD COLUMN "fallback_model_ids" jsonb NULL;
-- Modify "usage_records" table
ALTER TABLE "usage_records" ADD COLUMN "attempt" bigint NOT NULL DEFAULT 1;
