package kb

import (
	"context"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/store"
)

// D-27 / AC-04 召回回归：可见比例约 1%（走精确检索）与约 50%（走 HNSW + iterative_scan），
// 要求向量路返回满 k 条、没有越权命中、平均 recall@10 ≥ 0.95（以同一可见集合上的精确暴力结果为真值）。
// 需要带 pgvector 的 PostgreSQL：HENGDESK_PG_IT=1 HENGDESK_TEST_DATABASE_URL=...（会清空该库的 public schema）。
func TestPGRecallACLTiers(t *testing.T) {
	if os.Getenv("HENGDESK_PG_IT") != "1" {
		t.Skip("set HENGDESK_PG_IT=1")
	}
	c, db, err := store.Open(os.Getenv("HENGDESK_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if _, err := db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	const files, per, k = 1000, 10, 10
	rng := rand.New(rand.NewPCG(27, 1))
	centers := make([][]float32, 64)
	for i := range centers {
		centers[i] = randVec(rng, nil, 1)
	}
	vecs := map[int][][]float32{}
	s := PGStore{DB: db}
	for f := 1; f <= files; f++ {
		cs := make([]Chunk, per)
		for j := range cs {
			v := randVec(rng, centers[rng.IntN(len(centers))], 0.35)
			cs[j] = Chunk{OrgID: 1, KBID: 1, FileID: f, Piece: Piece{Seq: j, Content: "x"}, Embedding: v}
			vecs[f] = append(vecs[f], v)
		}
		if err := s.Replace(ctx, f, cs); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`ANALYZE kb_chunks`); err != nil {
		t.Fatal(err)
	}
	for _, tier := range []struct {
		name    string
		every   int
		want    string
		thresh  int
		minRec  float64
		queries int
	}{{"1%", 100, StrategyExact, 1000, 0.999, 30}, {"50%", 2, StrategyHNSW, 1000, 0.95, 30}} {
		var ids []int
		allowed := map[int]bool{}
		for f := 1; f <= files; f++ {
			if f%tier.every == 0 {
				ids = append(ids, f)
				allowed[f] = true
			}
		}
		var sum float64
		for qi := 0; qi < tier.queries; qi++ {
			q := randVec(rng, centers[rng.IntN(len(centers))], 0.35)
			hits, strategy, err := s.SearchVector(ctx, SearchReq{FileIDs: ids, VisibleChunks: len(ids) * per, Vec: q, PerRoute: k, ExactThreshold: tier.thresh})
			if err != nil {
				t.Fatal(err)
			}
			if strategy != tier.want {
				t.Fatalf("%s: strategy %s, want %s", tier.name, strategy, tier.want)
			}
			if len(hits) != k {
				t.Fatalf("%s: got %d hits, want %d（不允许静默低召回）", tier.name, len(hits), k)
			}
			// 真值：可见集合上的暴力排序（按 file_id、seq 定位）
			type cand struct {
				f, seq int
				d      float64
			}
			var all []cand
			for _, f := range ids {
				for j, v := range vecs[f] {
					all = append(all, cand{f, j, 1 - cosine(q, v)})
				}
			}
			sort.Slice(all, func(i, j int) bool { return all[i].d < all[j].d })
			truth := map[[2]int]bool{}
			for _, c := range all[:k] {
				truth[[2]int{c.f, c.seq}] = true
			}
			got := 0
			for _, h := range hits {
				if !allowed[h.FileID] {
					t.Fatalf("%s: 越权命中 file %d", tier.name, h.FileID)
				}
				if truth[[2]int{h.FileID, h.Seq}] {
					got++
				}
			}
			sum += float64(got) / k
		}
		// 计划检查：精确档走 B-tree 预过滤，HNSW 档走向量索引
		var plan string
		sqlText, idx := sqlExact, "kbchunk_file_id_seq"
		if tier.want == StrategyHNSW {
			sqlText, idx = sqlHNSW, "kbchunk_embedding_hnsw"
		}
		prow, err := db.Query(`EXPLAIN `+sqlText, vecLiteral(centers[0]), int64s(ids), k)
		if err != nil {
			t.Fatal(err)
		}
		for prow.Next() {
			var l string
			_ = prow.Scan(&l)
			plan += l + "\n"
		}
		_ = prow.Close()
		if !strings.Contains(plan, idx) {
			t.Fatalf("%s: 计划未使用 %s:\n%s", tier.name, idx, plan)
		}
		rec := sum / float64(tier.queries)
		t.Logf("tier %s: visible files=%d chunks=%d recall@%d=%.4f", tier.name, len(ids), len(ids)*per, k, rec)
		if rec < tier.minRec {
			t.Fatalf("%s: recall@%d %.4f < %.3f", tier.name, k, rec, tier.minRec)
		}
	}
	// 全文路同样预过滤
	if _, err := db.Exec(`UPDATE kb_chunks SET tsv = to_tsvector('simple', 'hello') WHERE file_id IN (1, 2)`); err != nil {
		t.Fatal(err)
	}
	th, err := s.SearchText(ctx, SearchReq{FileIDs: []int{2, 3}, Text: "hello"})
	if err != nil || len(th) != per {
		t.Fatalf("text hits %d %v", len(th), err)
	}
	for _, h := range th {
		if h.FileID != 2 {
			t.Fatalf("全文路越权命中 file %d", h.FileID)
		}
	}
}

func randVec(rng *rand.Rand, center []float32, noise float64) []float32 {
	v := make([]float32, Dim)
	var n float64
	for i := range v {
		x := rng.NormFloat64() * noise / math.Sqrt(Dim)
		if center != nil {
			x += float64(center[i])
		} else {
			x = rng.NormFloat64()
		}
		v[i] = float32(x)
		n += x * x
	}
	n = math.Sqrt(n)
	for i := range v {
		v[i] = float32(float64(v[i]) / n)
	}
	return v
}
