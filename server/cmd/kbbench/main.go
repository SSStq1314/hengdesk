// Command kbbench D-27 本地性能复测：在独立数据库里灌入 N 条 1024 维切片（默认 100 万），
// 按不同可见比例的用户跑完整检索链路（ACL 计算 → 可见文件 → 分档向量检索 + 全文检索 → RRF → 审计），
// 统计端到端与向量阶段（含 ACL 预过滤）的 P50 / P95 / P99，并抽样计算 recall@k。
//
// 问题嵌入由本地确定性函数代替（不含上游 embedding 模型的网络耗时）。
// 用法：go run ./cmd/kbbench -db postgres://.../kbbench -load
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/kb"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

const (
	dim     = kb.Dim
	nCenter = 256
	model   = "bench-embed"
)

var (
	dsn      = flag.String("db", "", "专用基准库 DSN（会清空 public schema）")
	load     = flag.Bool("load", false, "清库并灌数据、建索引")
	resume   = flag.Bool("resume", false, "与 -load 合用：不清库，从已写入的最大切片 id 续灌（-n 等参数须与首次相同；每批 1 万条一个 COPY 事务，中断后可续跑）")
	n        = flag.Int("n", 1_000_000, "切片总数")
	perFile  = flag.Int("per-file", 10, "每文件切片数")
	nKB      = flag.Int("kbs", 100, "知识库数（切片均分）")
	queries  = flag.Int("q", 200, "每档查询次数")
	recallN  = flag.Int("recall", 30, "每档抽样计算 recall 的查询数")
	topK     = flag.Int("k", kb.DefaultTopK, "top-k")
	ef       = flag.Int("ef", kb.DefaultEfSearch, "hnsw.ef_search")
	thresh   = flag.Int("threshold", kb.ExactThreshold, "精确 / HNSW 分档阈值（可见切片数）")
	out      = flag.String("out", "", "结果 JSON 输出路径")
	mwm      = flag.String("mwm", "4GB", "建 HNSW 索引时的 maintenance_work_mem")
	seedFlag = flag.Uint64("seed", 27, "随机种子")
	reindex  = flag.Bool("reindex", false, "不灌数据，只按当前迁移重建 halfvec HNSW 索引（D-42）")
	prewarm  = flag.Bool("prewarm", true, "跑基准前用 pg_prewarm 预热索引与表（D-42，模拟服务启动后的热缓存）")
	noiseF   = flag.Float64("noise", 0.03, "每维噪声标准差（0.03 时切片与簇中心余弦约 0.7，接近真实 embedding 的聚类程度；0.35 近似纯随机）")
)

func centers() [][]float32 {
	rng := rand.New(rand.NewPCG(*seedFlag, 1))
	cs := make([][]float32, nCenter)
	for i := range cs {
		cs[i] = randVec(rng, nil, 1)
	}
	return cs
}

func randVec(rng *rand.Rand, c []float32, nz float64) []float32 {
	v := make([]float32, dim)
	var s float64
	for i := range v {
		x := rng.NormFloat64() * nz
		if c != nil {
			x += float64(c[i])
		}
		v[i] = float32(x)
		s += x * x
	}
	s = math.Sqrt(s)
	for i := range v {
		v[i] = float32(float64(v[i]) / s)
	}
	return v
}

func vecText(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'f', 6, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// 合成数据：每个知识库 files 个文件；用户 u<pct> 对前 pct% 的知识库有 view。
var tiers = []struct {
	user int
	pct  int
}{{1, 1}, {2, 10}, {3, 50}, {4, 100}}

func doLoad(ctx context.Context, db *sql.DB) {
	files := *n / *perFile
	filesPerKB := files / *nKB
	from := 0
	if *resume {
		if err := db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM kb_chunks`).Scan(&from); err != nil {
			log.Fatal(err)
		}
		from -= from % 10_000 // 按整批续写，半批由 COPY 事务保证不会出现
		log.Printf("续灌：从第 %d 条开始", from)
	} else {
		initSchema(ctx, db, files, filesPerKB)
	}
	loadChunks(ctx, db, from, filesPerKB)
}

func initSchema(ctx context.Context, db *sql.DB, files, filesPerKB int) {
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		log.Fatal(err)
	}
	if _, err := store.ApplyMigrations(ctx, db); err != nil {
		log.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP INDEX IF EXISTS kbchunk_embedding_hnsw_half`); err != nil {
		log.Fatal(err)
	}
	now := time.Now()
	for k := 1; k <= *nKB; k++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO knowledge_bases (id, created_at, updated_at, org_id, name, embedding_model, created_by)
			VALUES ($1, $2, $2, 1, $3, $4, 1)`, k, now, fmt.Sprintf("kb-%d", k), model); err != nil {
			log.Fatal(err)
		}
	}
	for _, t := range tiers {
		for k := 1; k <= *nKB*t.pct/100; k++ {
			if _, err := db.ExecContext(ctx, `INSERT INTO acl (created_at, org_id, resource_type, resource_id, subject_type, subject_id, include_children, perms)
				VALUES ($1, 1, $2, $3, 'user', $4, false, $5)`, now, rbac.ResKB, strconv.Itoa(k), t.user, rbac.BitView|rbac.BitUse); err != nil {
				log.Fatal(err)
			}
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO kb_files (id, created_at, updated_at, org_id, kb_id, name, kind, size, sha256, object_key, status, chunk_count, uploaded_by, embedding_model, indexed_at)
		SELECT g, $1, $1, 1, (g - 1) / $2 + 1, 'f' || g, 'txt', 1, 'h' || g, 'o' || g, 'ready', $3, 1, $4, $1
		FROM generate_series(1, $5) g`, now, filesPerKB, *perFile, model, files); err != nil {
		log.Fatal(err)
	}
}

func loadChunks(ctx context.Context, db *sql.DB, from, filesPerKB int) {
	conn, err := pgx.Connect(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `DROP INDEX IF EXISTS kbchunk_embedding_hnsw_half`); err != nil { // 续灌时先去掉索引，灌完再建
		log.Fatal(err)
	}
	cs := centers()
	t0 := time.Now()
	const batch = 10_000
	for start := from; start < *n; start += batch {
		rng := rand.New(rand.NewPCG(*seedFlag, 2+uint64(start/batch))) // 每批独立种子，续灌结果与一次灌完一致
		var buf bytes.Buffer
		cnt := 0
		for i := start; i < min(start+batch, *n); i++ {
			f := i / *perFile + 1
			c := rng.IntN(nCenter)
			fmt.Fprintf(&buf, "%d\t1\t%d\t%d\t%d\tchunk %d topic t%d\t%s\t't%d' 'w%d' 'w%d'\n", i+1, (f-1)/filesPerKB+1, f, i%*perFile,
				i+1, c, vecText(randVec(rng, cs[c], *noiseF)), c, rng.IntN(5000), rng.IntN(5000))
			cnt++
		}
		if _, err := conn.PgConn().CopyFrom(ctx, &buf,
			`COPY kb_chunks (id, org_id, kb_id, file_id, seq, content, embedding, tsv) FROM STDIN`); err != nil {
			log.Fatal(err)
		}
		if (start/batch)%10 == 0 {
			log.Printf("已写入 %d / %d（%s）", start+cnt, *n, time.Since(t0).Round(time.Second))
		}
	}
	log.Printf("灌数据完成：%s", time.Since(t0).Round(time.Second))
	t1 := time.Now()
	for _, s := range []string{
		`SET maintenance_work_mem = '` + *mwm + `'`,
		`SET max_parallel_maintenance_workers = 7`,
		kb.HNSWIndexDDL,
		`VACUUM ANALYZE kb_chunks`, `ANALYZE kb_files`, `ANALYZE acl`, `ANALYZE knowledge_bases`,
		`SELECT setval(pg_get_serial_sequence('kb_chunks','id'), $1)`,
	} {
		var err error
		if strings.Contains(s, "$1") {
			_, err = conn.Exec(ctx, s, *n)
		} else {
			_, err = conn.Exec(ctx, s)
		}
		if err != nil {
			log.Fatalf("%s: %v", s, err)
		}
	}
	log.Printf("建 HNSW 索引 + ANALYZE：%s", time.Since(t1).Round(time.Second))
}

type stat struct {
	P50, P95, P99, Max float64
}

func pct(ds []time.Duration) stat {
	ms := make([]float64, len(ds))
	for i, d := range ds {
		ms[i] = float64(d.Microseconds()) / 1000
	}
	sort.Float64s(ms)
	at := func(p float64) float64 { return ms[min(len(ms)-1, int(math.Ceil(p*float64(len(ms))))-1)] }
	return stat{at(.50), at(.95), at(.99), ms[len(ms)-1]}
}

type tierResult struct {
	VisiblePct    int            `json:"visible_pct"`
	VisibleChunks int            `json:"visible_chunks"`
	Strategy      map[string]int `json:"strategy"`
	E2E           stat           `json:"e2e_ms"`
	Vector        stat           `json:"vector_ms"`
	RecallVec     float64        `json:"vector_recall_at_k"`
	Recall10      float64        `json:"vector_recall_at_10"`
	Leaks         int            `json:"leaks"`
}

func main() {
	flag.Parse()
	if *dsn == "" {
		log.Fatal("需要 -db")
	}
	ctx := context.Background()
	c, db, err := store.Open(*dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if *load {
		doLoad(ctx, db)
	} else if *reindex {
		t0 := time.Now()
		for _, q := range []string{`DROP INDEX IF EXISTS kbchunk_embedding_hnsw`, `DROP INDEX IF EXISTS kbchunk_embedding_hnsw_half`,
			`SET maintenance_work_mem = '` + *mwm + `'`, `SET max_parallel_maintenance_workers = 7`, kb.HNSWIndexDDL} {
			if _, err := db.ExecContext(ctx, q); err != nil {
				log.Fatalf("%s: %v", q, err)
			}
		}
		log.Printf("重建 halfvec HNSW：%s", time.Since(t0).Round(time.Second))
	}
	if *prewarm {
		t0 := time.Now()
		if err := kb.Prewarm(ctx, db); err != nil {
			log.Printf("预热失败（继续）：%v", err)
		} else {
			log.Printf("预热完成：%s", time.Since(t0).Round(time.Second))
		}
	}
	var total int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM kb_chunks`).Scan(&total); err != nil {
		log.Fatal(err)
	}
	filesPerKB := total / *perFile / *nKB
	cs := centers()
	var qrng *rand.Rand
	ret := &kb.Retriever{DB: c, AZ: &authz.Service{DB: c}, Store: benchStore{kb.PGStore{DB: db}}, Audit: audit.EntWriter{C: c},
		Embed: func(context.Context, string, string) ([]float32, error) {
			return randVec(qrng, cs[qrng.IntN(nCenter)], *noiseF), nil
		}}
	var results []tierResult
	for _, t := range tiers {
		qrng = rand.New(rand.NewPCG(*seedFlag, uint64(100+t.pct)))
		p := &auth.Principal{ID: t.user, OrgID: 1, Email: fmt.Sprintf("u%d@bench", t.user)}
		r := tierResult{VisiblePct: t.pct, Strategy: map[string]int{}}
		var e2e, vec []time.Duration
		maxFile := *nKB * t.pct / 100 * filesPerKB
		var recallSum, recall10Sum float64
		for i := 0; i < *queries+5; i++ {
			lastVec = nil
			q := fmt.Sprintf("t%d w%d", qrng.IntN(nCenter), qrng.IntN(5000))
			res, err := ret.Retrieve(ctx, p, nil, q, fmt.Sprintf("bench-%d-%d", t.pct, i), *topK)
			if err != nil {
				log.Fatal(err)
			}
			for _, cit := range res.Citations {
				if cit.FileID > maxFile {
					r.Leaks++
				}
			}
			if i < 5 { // 预热
				continue
			}
			r.VisibleChunks = res.Visible
			r.Strategy[res.Strategy]++
			e2e, vec = append(e2e, res.Total), append(vec, res.VectorDur)
			if i-5 < *recallN && lastVec != nil {
				a, b := vecRecall(ctx, db, lastVec, maxFile)
				recallSum += a
				recall10Sum += b
			}
		}
		r.E2E, r.Vector = pct(e2e), pct(vec)
		r.RecallVec = recallSum / float64(min(*recallN, *queries))
		r.Recall10 = recall10Sum / float64(min(*recallN, *queries))
		log.Printf("可见 %3d%%（%d 切片）策略 %v：端到端 P50 %.1f / P95 %.1f / P99 %.1f ms；向量阶段 P50 %.1f / P95 %.1f / P99 %.1f ms；向量 recall@%d %.4f / recall@10 %.4f；越权 %d",
			t.pct, r.VisibleChunks, r.Strategy, r.E2E.P50, r.E2E.P95, r.E2E.P99, r.Vector.P50, r.Vector.P95, r.Vector.P99, *perRouteK(), r.RecallVec, r.Recall10, r.Leaks)
		results = append(results, r)
	}
	if *out != "" {
		b, _ := json.MarshalIndent(map[string]any{"chunks": total, "k": *topK, "ef_search": *ef, "threshold": *thresh,
			"queries_per_tier": *queries, "tiers": results}, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			log.Fatal(err)
		}
	}
}

func perRouteK() *int { v := kb.DefaultPerRoute; return &v }

// benchStore 包一层 PGStore：记录向量路的查询向量与结果，并把分档阈值 / ef 换成命令行参数。
type benchStore struct{ kb.PGStore }

var (
	lastVec  []float32
	lastHits []kb.Hit
)

func (s benchStore) SearchVector(ctx context.Context, r kb.SearchReq) ([]kb.Hit, string, error) {
	r.ExactThreshold, r.EfSearch = *thresh, *ef
	h, st, err := s.PGStore.SearchVector(ctx, r)
	lastVec, lastHits = r.Vec, h
	return h, st, err
}

// vecRecall 向量路 recall@PerRoute 与 recall@10：与同一可见范围内的精确（float32）top-N 比较（file_id ≤ maxFile 即可见集合）。
func vecRecall(ctx context.Context, db *sql.DB, v []float32, maxFile int) (float64, float64) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, s := range []string{`SET LOCAL enable_indexscan = off`, `SET LOCAL max_parallel_workers_per_gather = 8`} {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			log.Fatal(err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM kb_chunks WHERE file_id <= $2 ORDER BY embedding <=> $1::vector LIMIT $3`,
		vecText(v), maxFile, kb.DefaultPerRoute)
	if err != nil {
		log.Fatal(err)
	}
	want, want10 := map[int64]bool{}, map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			log.Fatal(err)
		}
		if len(want) < 10 {
			want10[id] = true
		}
		want[id] = true
	}
	_ = rows.Close()
	got, got10 := 0, 0
	for i, h := range lastHits {
		if want[int64(h.ChunkID)] {
			got++
		}
		if i < 10 && want10[int64(h.ChunkID)] {
			got10++
		}
	}
	return float64(got) / float64(len(want)), float64(got10) / float64(len(want10))
}
