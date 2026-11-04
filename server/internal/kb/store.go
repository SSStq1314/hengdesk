package kb

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Dim 向量维度固定 1024（待确认 1）；与 kb_chunks.embedding vector(1024) 一致。
const Dim = 1024

// Chunk 写入存储的一条切片。
type Chunk struct {
	OrgID, KBID, FileID int
	Piece
	Embedding []float32
}

// Hit 检索命中。
type Hit struct {
	ChunkID int64
	FileID  int
	KBID    int
	Seq     int
	Content string
	Heading string
	Start   int
	End     int
	Score   float64 // 向量路：余弦相似度；全文路：ts_rank；融合后：RRF 分
}

// ChunkStore chunk 存储。ACL 不落在 chunk 上：检索时由调用方先算出 allowed file IDs 传入（D-27），
// 改 ACL 只改元数据、不重建索引。
type ChunkStore interface {
	// Replace 原子地替换一个文件的全部切片。
	Replace(ctx context.Context, fileID int, chunks []Chunk) error
	DeleteFile(ctx context.Context, fileID int) error
	DeleteKB(ctx context.Context, kbID int) error
	CountFile(ctx context.Context, fileID int) (int, error)
}

// PGStore PostgreSQL + pgvector 实现（原生 SQL；向量以文本字面量传入后 ::vector 转换，不引入额外依赖）。
type PGStore struct{ DB *sql.DB }

func vecLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', 7, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func (s PGStore) Replace(ctx context.Context, fileID int, chunks []Chunk) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM kb_chunks WHERE file_id = $1`, fileID); err != nil {
		return err
	}
	const batch = 100
	for i := 0; i < len(chunks); i += batch {
		part := chunks[i:min(i+batch, len(chunks))]
		var q strings.Builder
		q.WriteString(`INSERT INTO kb_chunks (org_id, kb_id, file_id, seq, content, heading, char_start, char_end, embedding, tsv) VALUES `)
		args := make([]any, 0, len(part)*10)
		for j, c := range part {
			if len(c.Embedding) != Dim {
				return fmt.Errorf("embedding dim %d != %d", len(c.Embedding), Dim)
			}
			if c.FileID != fileID {
				return fmt.Errorf("chunk file %d != %d", c.FileID, fileID)
			}
			if j > 0 {
				q.WriteByte(',')
			}
			n := len(args)
			fmt.Fprintf(&q, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d::vector,to_tsvector('simple',$%d))",
				n+1, n+2, n+3, n+4, n+5, n+6, n+7, n+8, n+9, n+10)
			args = append(args, c.OrgID, c.KBID, c.FileID, c.Seq, c.Content, c.Heading, c.Start, c.End,
				vecLiteral(c.Embedding), TSText(c.Heading+"\n"+c.Content))
		}
		if _, err := tx.ExecContext(ctx, q.String(), args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s PGStore) DeleteFile(ctx context.Context, fileID int) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM kb_chunks WHERE file_id = $1`, fileID)
	return err
}

func (s PGStore) DeleteKB(ctx context.Context, kbID int) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM kb_chunks WHERE kb_id = $1`, kbID)
	return err
}

func (s PGStore) CountFile(ctx context.Context, fileID int) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM kb_chunks WHERE file_id = $1`, fileID).Scan(&n)
	return n, err
}

// MemStore 内存实现：SQLite 单元测试与无 pgvector 的开发环境使用（检索为精确暴力计算）。
type MemStore struct {
	mu     sync.RWMutex
	nextID int64
	rows   map[int][]memRow // file_id → chunks
}

type memRow struct {
	id    int64
	c     Chunk
	terms map[string]int
}

func NewMemStore() *MemStore { return &MemStore{rows: map[int][]memRow{}} }

func (m *MemStore) Replace(_ context.Context, fileID int, chunks []Chunk) error {
	rows := make([]memRow, 0, len(chunks))
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range chunks {
		if len(c.Embedding) != Dim {
			return fmt.Errorf("embedding dim %d != %d", len(c.Embedding), Dim)
		}
		m.nextID++
		tm := map[string]int{}
		for _, t := range Terms(c.Heading + "\n" + c.Content) {
			tm[t]++
		}
		rows = append(rows, memRow{id: m.nextID, c: c, terms: tm})
	}
	if len(rows) == 0 {
		delete(m.rows, fileID)
	} else {
		m.rows[fileID] = rows
	}
	return nil
}

func (m *MemStore) DeleteFile(_ context.Context, fileID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, fileID)
	return nil
}

func (m *MemStore) DeleteKB(_ context.Context, kbID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for f, rs := range m.rows {
		if len(rs) > 0 && rs[0].c.KBID == kbID {
			delete(m.rows, f)
		}
	}
	return nil
}

func (m *MemStore) CountFile(_ context.Context, fileID int) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rows[fileID]), nil
}

// Total 全部切片数（测试用）。
func (m *MemStore) Total() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, rs := range m.rows {
		n += len(rs)
	}
	return n
}
