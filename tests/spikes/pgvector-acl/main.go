// pgvector + ACL 预过滤性能 spike。
// 用法：go run . -cmd load|index|bench  (DSN 用 -dsn 或 PGURL)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

var (
	dsn      = flag.String("dsn", "postgres://postgres@/hengdesk_spike?host=/tmp&port=55432", "dsn")
	cmd      = flag.String("cmd", "bench", "load|index|bench")
	nDocs    = flag.Int("docs", 30000, "documents")
	perDoc   = flag.Int("chunks", 10, "chunks per doc")
	dim      = flag.Int("dim", 768, "dim")
	nKB      = flag.Int("kbs", 50, "knowledge bases")
	nQ       = flag.Int("q", 200, "queries per config")
	k        = flag.Int("k", 10, "top k")
	only     = flag.String("only", "", "only run configs whose name contains this")
	out      = flag.String("out", "results.json", "results file")
	clusters = 1000
)

// 主体编号：1/2/3 = 可见比例 1%/10%/50% 的「组」（模拟部门/角色）；100+d = 部门；1000+u = 个人
var ratioPrincipal = map[string]int32{"1%": 1, "10%": 2, "50%": 3}

func rng(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

func centers() [][]float32 {
	r := rng(42)
	c := make([][]float32, clusters)
	for i := range c {
		c[i] = make([]float32, *dim)
		for j := range c[i] {
			c[i][j] = float32(r.NormFloat64())
		}
		norm(c[i])
	}
	return c
}

func norm(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	s = math.Sqrt(s)
	for i := range v {
		v[i] = float32(float64(v[i]) / s)
	}
}

func point(r *rand.Rand, c [][]float32, sigma float64) []float32 {
	cc := c[r.Intn(len(c))]
	v := make([]float32, *dim)
	for j := range v {
		v[j] = cc[j] + float32(r.NormFloat64()*sigma/math.Sqrt(float64(*dim)))
	}
	norm(v)
	return v
}

func exec(ctx context.Context, p *pgxpool.Pool, sqls ...string) {
	for _, s := range sqls {
		t := time.Now()
		if _, err := p.Exec(ctx, s); err != nil {
			log.Fatalf("%s: %v", s, err)
		}
		log.Printf("%-90.90s %v", strings.ReplaceAll(s, "\n", " "), time.Since(t).Round(time.Millisecond))
	}
}

func load(ctx context.Context, p *pgxpool.Pool) {
	exec(ctx, p, `DROP TABLE IF EXISTS chunks, chunks_kb, docs, doc_acl, user_visible_docs CASCADE`,
		`CREATE TABLE docs(id int PRIMARY KEY, kb_id int NOT NULL, bucket float8 NOT NULL)`,
		`CREATE TABLE doc_acl(doc_id int NOT NULL, principal int NOT NULL, PRIMARY KEY(principal, doc_id))`,
		fmt.Sprintf(`CREATE TABLE chunks(id bigint PRIMARY KEY, doc_id int NOT NULL, kb_id int NOT NULL, acl int[] NOT NULL, emb vector(%d) NOT NULL)`, *dim),
		`ALTER TABLE chunks ALTER COLUMN emb SET STORAGE PLAIN`)
	r := rng(7)
	var docRows, aclRows [][]any
	docACL := map[int][]int32{}
	for d := 0; d < *nDocs; d++ {
		b := r.Float64()
		kb := d % *nKB
		docRows = append(docRows, []any{int32(d), int32(kb), b})
		acl := []int32{100 + int32(r.Intn(20)), 1000 + int32(r.Intn(5000))}
		if b < 0.01 {
			acl = append(acl, 1)
		}
		if b < 0.10 {
			acl = append(acl, 2)
		}
		if b < 0.50 {
			acl = append(acl, 3)
		}
		docACL[d] = acl
		for _, a := range acl {
			aclRows = append(aclRows, []any{int32(d), a})
		}
	}
	if _, err := p.CopyFrom(ctx, pgx.Identifier{"docs"}, []string{"id", "kb_id", "bucket"}, pgx.CopyFromRows(docRows)); err != nil {
		log.Fatal(err)
	}
	if _, err := p.CopyFrom(ctx, pgx.Identifier{"doc_acl"}, []string{"doc_id", "principal"}, pgx.CopyFromRows(aclRows)); err != nil {
		log.Fatal(err)
	}
	c := centers()
	t := time.Now()
	total := *nDocs * *perDoc
	src := &chunkSrc{r: rng(9), c: c, total: total, acl: docACL}
	n, err := p.CopyFrom(ctx, pgx.Identifier{"chunks"}, []string{"id", "doc_id", "kb_id", "acl", "emb"}, src)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("copied %d chunks in %v", n, time.Since(t))
	// 物化「用户可见文档集合」：这里每个测试用户 = 一个比例组
	exec(ctx, p, `CREATE TABLE user_visible_docs(user_id int NOT NULL, doc_id int NOT NULL, PRIMARY KEY(user_id, doc_id))`,
		`INSERT INTO user_visible_docs SELECT principal, doc_id FROM doc_acl WHERE principal IN (1,2,3)`,
		`CREATE INDEX ON chunks(doc_id)`,
		`CREATE INDEX ON chunks USING gin(acl)`,
		`CREATE INDEX ON chunks(kb_id)`,
		`ANALYZE`)
}

type chunkSrc struct {
	r     *rand.Rand
	c     [][]float32
	i     int
	total int
	acl   map[int][]int32
	row   []any
}

func (s *chunkSrc) Next() bool {
	if s.i >= s.total {
		return false
	}
	d := s.i / *perDoc
	s.row = []any{int64(s.i), int32(d), int32(d % *nKB), s.acl[d], pgvector.NewVector(point(s.r, s.c, 0.9))}
	s.i++
	if s.i%50000 == 0 {
		log.Printf("  %d/%d", s.i, s.total)
	}
	return true
}
func (s *chunkSrc) Values() ([]any, error) { return s.row, nil }
func (s *chunkSrc) Err() error             { return nil }

func index(ctx context.Context, p *pgxpool.Pool) {
	exec(ctx, p, `SET maintenance_work_mem='2GB'`, `SET max_parallel_maintenance_workers=6`)
	exec(ctx, p, `CREATE INDEX IF NOT EXISTS chunks_hnsw ON chunks USING hnsw (emb vector_cosine_ops) WITH (m=16, ef_construction=64)`)
	// 方案 B：按知识库 LIST 分区，每分区一个 HNSW
	exec(ctx, p, fmt.Sprintf(`CREATE TABLE chunks_kb(id bigint, doc_id int, kb_id int, acl int[], emb vector(%d)) PARTITION BY LIST(kb_id)`, *dim))
	for i := 0; i < *nKB; i++ {
		exec(ctx, p, fmt.Sprintf(`CREATE TABLE chunks_kb_%d PARTITION OF chunks_kb FOR VALUES IN (%d)`, i, i))
	}
	exec(ctx, p, `INSERT INTO chunks_kb SELECT * FROM chunks`,
		`CREATE INDEX ON chunks_kb USING hnsw (emb vector_cosine_ops) WITH (m=16, ef_construction=64)`,
		`CREATE INDEX ON chunks_kb USING gin(acl)`,
		`ANALYZE`)
}

type cfg struct {
	Name  string
	Ratio string
	Set   []string
	SQL   string // $1 = query vec, $2 = principals int[] / user id, $3 = kb
	Arg   func(ratio string) any
	KB    bool
}

type res struct {
	Name, Ratio, Plan   string
	P50, P95, P99, Mean float64 // ms
	Recall              float64
	AvgReturned         float64
	N                   int
}

func pct(xs []float64, p float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(math.Ceil(p*float64(len(s)))) - 1
	if i < 0 {
		i = 0
	}
	return s[i]
}

func runQ(ctx context.Context, conn *pgxpool.Conn, sql string, args ...any) ([]int64, error) {
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

func bench(ctx context.Context, p *pgxpool.Pool) {
	c := centers()
	qr := rng(12345)
	qs := make([]pgvector.Vector, *nQ)
	for i := range qs {
		qs[i] = pgvector.NewVector(point(qr, c, 0.9))
	}
	prin := func(r string) any { return []int32{ratioPrincipal[r]} }
	uid := func(r string) any { return ratioPrincipal[r] }
	lim := fmt.Sprintf(" ORDER BY emb <=> $1 LIMIT %d", *k)
	acl := `SELECT id FROM chunks WHERE acl && $2::int[]` + lim
	mat := `SELECT id FROM chunks WHERE doc_id IN (SELECT doc_id FROM user_visible_docs WHERE user_id=$2)` + lim
	join := `SELECT id FROM chunks WHERE doc_id IN (SELECT doc_id FROM doc_acl WHERE principal = ANY($2::int[]))` + lim
	kbF := `SELECT id FROM chunks WHERE kb_id=$3 AND acl && $2::int[]` + lim
	kbP := `SELECT id FROM chunks_kb WHERE kb_id=$3 AND acl && $2::int[]` + lim
	exact := []string{"SET enable_indexscan=off"} // 禁用 HNSW（及 btree index scan），走 bitmap/seq + 精确排序
	reset := []string{"SET plan_cache_mode=force_custom_plan", "RESET enable_indexscan", "RESET enable_bitmapscan", "RESET enable_seqscan", "RESET hnsw.iterative_scan", "RESET hnsw.ef_search", "RESET hnsw.max_scan_tuples"}
	hn := func(ef int, it string) []string {
		s := []string{fmt.Sprintf("SET hnsw.ef_search=%d", ef), "SET hnsw.iterative_scan=" + it, "SET enable_bitmapscan=off", "SET enable_seqscan=off"}
		return s
	}
	var cfgs []cfg
	for _, r := range []string{"1%", "10%", "50%"} {
		cfgs = append(cfgs,
			cfg{Name: "exact/acl-array(GIN)+sort", Ratio: r, Set: exact, SQL: acl, Arg: prin},
			cfg{Name: "exact/materialized+sort", Ratio: r, Set: exact, SQL: mat, Arg: uid},
			cfg{Name: "planner-default/acl-array", Ratio: r, SQL: acl, Arg: prin},
		)
		for _, ef := range []int{40, 100, 200} {
			for _, it := range []string{"off", "relaxed_order", "strict_order"} {
				cfgs = append(cfgs, cfg{Name: fmt.Sprintf("hnsw/acl-array ef=%d iter=%s", ef, it), Ratio: r, Set: hn(ef, it), SQL: acl, Arg: prin})
			}
		}
		cfgs = append(cfgs,
			cfg{Name: "hnsw/materialized ef=100 iter=relaxed_order", Ratio: r, Set: hn(100, "relaxed_order"), SQL: mat, Arg: uid},
			cfg{Name: "hnsw/acl-join ef=100 iter=relaxed_order", Ratio: r, Set: hn(100, "relaxed_order"), SQL: join, Arg: prin},
			cfg{Name: "kb:exact/one-kb(unpartitioned)", Ratio: r, Set: exact, SQL: kbF, Arg: prin, KB: true},
			cfg{Name: "kb:hnsw/one-kb(unpartitioned) ef=100 relaxed", Ratio: r, Set: hn(100, "relaxed_order"), SQL: kbF, Arg: prin, KB: true},
			cfg{Name: "kb:hnsw/partitioned-by-kb ef=40 off", Ratio: r, Set: hn(40, "off"), SQL: kbP, Arg: prin, KB: true},
			cfg{Name: "kb:hnsw/partitioned-by-kb ef=100 relaxed", Ratio: r, Set: hn(100, "relaxed_order"), SQL: kbP, Arg: prin, KB: true},
			cfg{Name: "kb:exact/partitioned-by-kb", Ratio: r, Set: exact, SQL: kbP, Arg: prin, KB: true},
		)
	}
	conn, err := p.Acquire(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Release()
	gt := map[string][][]int64{}
	truth := func(ratio string, kb bool) [][]int64 {
		key := fmt.Sprint(ratio, kb)
		if g, ok := gt[key]; ok {
			return g
		}
		for _, s := range append(reset, exact...) {
			conn.Exec(ctx, s)
		}
		g := make([][]int64, *nQ)
		for i, q := range qs {
			var err error
			if kb {
				g[i], err = runQ(ctx, conn, kbF, q, prin(ratio), int32(i%*nKB))
			} else {
				g[i], err = runQ(ctx, conn, acl, q, prin(ratio))
			}
			if err != nil {
				log.Fatal(err)
			}
		}
		gt[key] = g
		return g
	}
	var results []res
	for _, cf := range cfgs {
		if *only != "" && !strings.Contains(cf.Name, *only) {
			continue
		}
		g := truth(cf.Ratio, cf.KB)
		for _, s := range append(reset, cf.Set...) {
			if _, err := conn.Exec(ctx, s); err != nil {
				log.Fatal(s, err)
			}
		}
		args := func(i int) []any {
			a := []any{qs[i], cf.Arg(cf.Ratio)}
			if cf.KB {
				a = append(a, int32(i%*nKB))
			}
			return a
		}
		// plan
		var planLines []string
		rows, _ := conn.Query(ctx, "EXPLAIN (COSTS OFF) "+cf.SQL, args(0)...)
		for rows.Next() {
			var l string
			rows.Scan(&l)
			planLines = append(planLines, strings.TrimSpace(l))
		}
		rows.Close()
		plan := summarizePlan(planLines)
		for i := 0; i < 10; i++ { // warmup
			runQ(ctx, conn, cf.SQL, args(i)...)
		}
		var lat []float64
		var hit, ret, want float64
		for i := range qs {
			t := time.Now()
			ids, err := runQ(ctx, conn, cf.SQL, args(i)...)
			if err != nil {
				log.Fatal(cf.Name, err)
			}
			lat = append(lat, float64(time.Since(t).Microseconds())/1000)
			set := map[int64]bool{}
			for _, id := range ids {
				set[id] = true
			}
			for _, id := range g[i] {
				if set[id] {
					hit++
				}
			}
			want += float64(len(g[i]))
			ret += float64(len(ids))
		}
		var sum float64
		for _, l := range lat {
			sum += l
		}
		r := res{Name: cf.Name, Ratio: cf.Ratio, Plan: plan, P50: pct(lat, .5), P95: pct(lat, .95), P99: pct(lat, .99), Mean: sum / float64(len(lat)), Recall: hit / want, AvgReturned: ret / float64(len(qs)), N: len(qs)}
		results = append(results, r)
		log.Printf("%-4s %-48s p50=%7.2f p95=%7.2f recall=%.3f ret=%.1f plan=%s", r.Ratio, r.Name, r.P50, r.P95, r.Recall, r.AvgReturned, r.Plan)
	}
	b, _ := json.MarshalIndent(results, "", " ")
	os.WriteFile(*out, b, 0o644)
}

func summarizePlan(ls []string) string {
	var keep []string
	for _, l := range ls {
		l = strings.TrimPrefix(l, "->  ")
		for _, kw := range []string{"Index Scan", "Bitmap", "Seq Scan", "Sort", "Hash", "Nested", "Append", "Merge"} {
			if strings.Contains(l, kw) {
				if i := strings.Index(l, "  "); i > 0 {
					l = l[:i]
				}
				keep = append(keep, l)
				break
			}
		}
	}
	return strings.Join(keep, " > ")
}

func main() {
	flag.Parse()
	ctx := context.Background()
	pc, err := pgxpool.ParseConfig(*dsn)
	if err != nil {
		log.Fatal(err)
	}
	pc.AfterConnect = func(ctx context.Context, c *pgx.Conn) error { return pgxvec.RegisterTypes(ctx, c) }
	// 关键：禁用 pgx 语句缓存，否则 PG 会复用在 enable_indexscan=off 下生成的 generic plan，SET 不生效（首轮 bench 踩到，见 REPORT）
	pc.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	pc.MaxConns = 4
	p, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		log.Fatal(err)
	}
	defer p.Close()
	switch *cmd {
	case "load":
		load(ctx, p)
	case "index":
		index(ctx, p)
	case "bench":
		bench(ctx, p)
	}
}
