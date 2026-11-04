package kb

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"
)

// 检索默认值（m1-block4.md 待确认 4；ef_search 按 D-43 改为 1000）。
const (
	DefaultTopK      = 8
	DefaultPerRoute  = 40
	RRFK             = 60
	ExactThreshold   = 20000 // 可见 chunk 数不超过它时走精确检索
	DefaultEfSearch  = 1000  // D-43：100 万复测中 400 时 50% 可见 recall@10 只有 0.89，1000 时 0.953
	StrategyExact    = "exact"
	StrategyHNSW     = "hnsw"
	StrategyFallback = "hnsw_fallback_exact"
)

// SearchReq 一次检索。FileIDs 是已按 ACL 算好的可见文件（D-27：同一条 SQL 里 file_id = ANY 预过滤）。
type SearchReq struct {
	FileIDs        []int
	VisibleChunks  int // 可见文件 chunk_count 之和，用于分档
	Vec            []float32
	Text           string
	PerRoute       int
	ExactThreshold int
	EfSearch       int
}

// SearchStats 检索过程信息（写入 kb.retrieve 审计）。
type SearchStats struct {
	Strategy  string
	VecHits   int
	TextHits  int
	VectorDur time.Duration // 向量阶段（含 ACL 预过滤）耗时
	TextDur   time.Duration
}

// Searcher 由 ChunkStore 实现的检索能力。
type Searcher interface {
	SearchVector(ctx context.Context, r SearchReq) ([]Hit, string, error)
	SearchText(ctx context.Context, r SearchReq) ([]Hit, error)
}

func (r *SearchReq) norm() {
	if r.PerRoute <= 0 {
		r.PerRoute = DefaultPerRoute
	}
	if r.ExactThreshold <= 0 {
		r.ExactThreshold = ExactThreshold
	}
	if r.EfSearch <= 0 {
		r.EfSearch = DefaultEfSearch
	}
	if r.EfSearch < r.PerRoute {
		r.EfSearch = r.PerRoute
	}
}

// Hybrid 向量 + 全文两路召回，RRF 融合后取前 k 条。
func Hybrid(ctx context.Context, s Searcher, r SearchReq, k int) ([]Hit, SearchStats, error) {
	r.norm()
	var st SearchStats
	if len(r.FileIDs) == 0 {
		return nil, st, nil
	}
	t0 := time.Now()
	vh, strategy, err := s.SearchVector(ctx, r)
	if err != nil {
		return nil, st, err
	}
	st.VectorDur, st.Strategy, st.VecHits = time.Since(t0), strategy, len(vh)
	t1 := time.Now()
	th, err := s.SearchText(ctx, r)
	if err != nil {
		return nil, st, err
	}
	st.TextDur, st.TextHits = time.Since(t1), len(th)
	return RRF(k, vh, th), st, nil
}

// RRF Reciprocal Rank Fusion：score = Σ 1/(RRFK + rank)。
func RRF(k int, lists ...[]Hit) []Hit {
	score := map[int64]float64{}
	first := map[int64]Hit{}
	for _, l := range lists {
		for i, h := range l {
			score[h.ChunkID] += 1 / float64(RRFK+i+1)
			if _, ok := first[h.ChunkID]; !ok {
				first[h.ChunkID] = h
			}
		}
	}
	out := make([]Hit, 0, len(first))
	for id, h := range first {
		h.Score = score[id]
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

func int64s(ids []int) []int64 {
	out := make([]int64, len(ids))
	for i, v := range ids {
		out[i] = int64(v)
	}
	return out
}

const hitCols = `id, file_id, kb_id, seq, content, heading, char_start, char_end`

const (
	sqlExact = `SELECT ` + hitCols + `, 1 - (embedding <=> $1::vector) AS sim FROM kb_chunks
		WHERE file_id = ANY($2) ORDER BY (embedding <=> $1::vector) + 0 LIMIT $3`
	// sqlHNSW D-42：HNSW 建在 halfvec 表达式上（索引约减半），内层按半精度距离走索引取候选，
	// 外层按原始 float32 距离重排并返回相似度。
	sqlHNSW = `WITH c AS MATERIALIZED (
		SELECT ` + hitCols + `, embedding FROM kb_chunks
		WHERE file_id + 0 = ANY($2) ORDER BY embedding::halfvec(1024) <=> $1::text::halfvec(1024) LIMIT $3)
		SELECT ` + hitCols + `, 1 - (embedding <=> $1::text::vector) AS sim FROM c ORDER BY embedding <=> $1::text::vector, id`
)

func scanHits(rows *sql.Rows) ([]Hit, error) {
	defer func() { _ = rows.Close() }()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ChunkID, &h.FileID, &h.KBID, &h.Seq, &h.Content, &h.Heading, &h.Start, &h.End, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SearchVector D-27 分档：可见 chunk 少走精确检索（file_id 索引预过滤 + 距离排序，「+0」阻止规划器走 HNSW）；
// 多走 HNSW（「file_id + 0」阻止规划器改用 B-tree，iterative_scan=relaxed_order，ef_search 默认 1000，halfvec 索引），外层按距离重排。HNSW 返回不足
// min(k, 可见数) 时回退精确检索，不允许静默低召回。force_custom_plan 避免预编译语句用通用计划。
func (s PGStore) SearchVector(ctx context.Context, r SearchReq) ([]Hit, string, error) {
	r.norm()
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL plan_cache_mode = force_custom_plan`); err != nil {
		return nil, "", err
	}
	vec, ids := vecLiteral(r.Vec), int64s(r.FileIDs)
	exact := func() ([]Hit, error) {
		rows, err := tx.QueryContext(ctx, sqlExact, vec, ids, r.PerRoute)
		if err != nil {
			return nil, err
		}
		return scanHits(rows)
	}
	if r.VisibleChunks <= r.ExactThreshold {
		h, err := exact()
		return h, StrategyExact, err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`SET LOCAL hnsw.ef_search = %d`, r.EfSearch)); err != nil {
		return nil, "", err
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
		return nil, "", err
	}
	rows, err := tx.QueryContext(ctx, sqlHNSW, vec, ids, r.PerRoute)
	if err != nil {
		return nil, "", err
	}
	h, err := scanHits(rows)
	if err != nil {
		return nil, "", err
	}
	if len(h) < min(r.PerRoute, r.VisibleChunks) {
		h, err = exact()
		return h, StrategyFallback, err
	}
	return h, StrategyHNSW, nil
}

// SearchText 全文路：gse 分词后的 OR 查询，ts_rank 排序；同样 file_id = ANY 预过滤。
func (s PGStore) SearchText(ctx context.Context, r SearchReq) ([]Hit, error) {
	r.norm()
	q := TSQuery(r.Text)
	if q == "" {
		return nil, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+hitCols+`, ts_rank(tsv, q) AS rank
		FROM kb_chunks, to_tsquery('simple', $1) q
		WHERE file_id = ANY($2) AND tsv @@ q ORDER BY rank DESC, id LIMIT $3`, q, int64s(r.FileIDs), r.PerRoute)
	if err != nil {
		return nil, err
	}
	return scanHits(rows)
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

func (m *MemStore) visible(ids []int, fn func(memRow)) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, id := range ids {
		for _, r := range m.rows[id] {
			fn(r)
		}
	}
}

func memHit(r memRow, score float64) Hit {
	return Hit{ChunkID: r.id, FileID: r.c.FileID, KBID: r.c.KBID, Seq: r.c.Seq, Content: r.c.Content,
		Heading: r.c.Heading, Start: r.c.Start, End: r.c.End, Score: score}
}

func topHits(hs []Hit, n int) []Hit {
	sort.Slice(hs, func(i, j int) bool {
		if hs[i].Score != hs[j].Score {
			return hs[i].Score > hs[j].Score
		}
		return hs[i].ChunkID < hs[j].ChunkID
	})
	if len(hs) > n {
		hs = hs[:n]
	}
	return hs
}

// SearchVector 内存实现：始终精确计算。
func (m *MemStore) SearchVector(_ context.Context, r SearchReq) ([]Hit, string, error) {
	r.norm()
	var hs []Hit
	m.visible(r.FileIDs, func(row memRow) { hs = append(hs, memHit(row, cosine(r.Vec, row.c.Embedding))) })
	return topHits(hs, r.PerRoute), StrategyExact, nil
}

// SearchText 内存实现：命中词数加权。
func (m *MemStore) SearchText(_ context.Context, r SearchReq) ([]Hit, error) {
	r.norm()
	terms := Terms(r.Text)
	if len(terms) == 0 {
		return nil, nil
	}
	var hs []Hit
	m.visible(r.FileIDs, func(row memRow) {
		s := 0.0
		for _, t := range terms {
			if n := row.terms[t]; n > 0 {
				s += 1 + math.Log(float64(n))
			}
		}
		if s > 0 {
			hs = append(hs, memHit(row, s))
		}
	})
	return topHits(hs, r.PerRoute), nil
}

// 编译期检查。
var (
	_ Searcher = PGStore{}
	_ Searcher = (*MemStore)(nil)
)
