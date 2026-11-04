package kb

import (
	"bufio"
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
)

// HNSWIndexName / HNSWIndexDDL D-42：向量索引建在 halfvec 表达式上（与迁移 20261120000000_m1_kb_d42.sql 一致）。
const (
	HNSWIndexName = "kbchunk_embedding_hnsw_half"
	HNSWIndexDDL  = `CREATE INDEX IF NOT EXISTS kbchunk_embedding_hnsw_half ON kb_chunks USING hnsw ((embedding::halfvec(1024)) halfvec_cosine_ops) WITH (m = 16, ef_construction = 64)`
)

// prewarmRels 预热顺序：先向量索引（最大、对 HNSW 档延迟影响最大），再 B-tree 与表本身。
var prewarmRels = []string{HNSWIndexName, "kbchunk_file_id_seq", "kb_chunks"}

// Prewarm D-42：用 pg_prewarm 把向量索引与 kb_chunks 读入 shared_buffers / OS 缓存。
// 尽力而为：扩展不可用或权限不足时返回错误，由调用方记日志，不影响服务启动。
func Prewarm(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pg_prewarm`); err != nil {
		return err
	}
	for _, rel := range prewarmRels {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, rel).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		if _, err := db.ExecContext(ctx, `SELECT pg_prewarm($1::regclass)`, rel); err != nil {
			return err
		}
	}
	return nil
}

// IndexStatus 向量索引体积与机器内存（D-42：索引超过内存 50% 时管理后台提示）。
type IndexStatus struct {
	IndexBytes  int64 `json:"index_bytes"`
	MemoryBytes int64 `json:"memory_bytes"`
	OverHalf    bool  `json:"over_half"`
}

// IndexSize 当前 HNSW 索引字节数；索引不存在时为 0。
func IndexSize(ctx context.Context, db *sql.DB) (int64, error) {
	var n sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT pg_relation_size(to_regclass($1))`, HNSWIndexName).Scan(&n)
	return n.Int64, err
}

// CheckIndex memBytes ≤ 0 时读本机 /proc/meminfo（数据库与服务同机部署的推荐形态；分机部署应在配置里显式给出数据库机内存）。
func CheckIndex(ctx context.Context, db *sql.DB, memBytes int64) (IndexStatus, error) {
	if memBytes <= 0 {
		memBytes = HostMemory()
	}
	sz, err := IndexSize(ctx, db)
	if err != nil {
		return IndexStatus{}, err
	}
	return IndexStatus{IndexBytes: sz, MemoryBytes: memBytes, OverHalf: memBytes > 0 && sz*2 > memBytes}, nil
}

// HostMemory 读 /proc/meminfo 的 MemTotal；读不到返回 0（不提示）。
func HostMemory() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) >= 2 && fs[0] == "MemTotal:" {
			kb, _ := strconv.ParseInt(fs[1], 10, 64)
			return kb * 1024
		}
	}
	return 0
}
