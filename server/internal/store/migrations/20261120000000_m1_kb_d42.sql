-- D-42：HNSW 索引改为 halfvec 表达式索引（索引约减半，便于整体驻留内存）。
-- 列仍是 float32 vector(1024)：精确档与 HNSW 档外层重排都用原始精度，只有 HNSW 候选阶段用半精度。
-- 升级时会重建索引（100 万条约 10–15 分钟，期间 HNSW 档检索走顺序扫描，结果仍正确）。
DROP INDEX IF EXISTS "kbchunk_embedding_hnsw";
CREATE INDEX "kbchunk_embedding_hnsw_half" ON "kb_chunks" USING hnsw ((embedding::halfvec(1024)) halfvec_cosine_ops) WITH (m = 16, ef_construction = 64);
