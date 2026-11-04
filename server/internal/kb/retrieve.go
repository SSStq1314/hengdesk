package kb

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/egress"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/kbfile"
	"github.com/SSStq1314/hengdesk/server/internal/ent/knowledgebase"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/usage"
)

// ErrRetrieveAudit 检索审计写入失败：按「审计写失败就拒绝执行」，调用方不得使用检索结果。
var ErrRetrieveAudit = errors.New("kb.retrieve 审计写入失败")

// Citation 一条可引用的检索结果。Index 从 1 开始，对应上下文中的 [n]。
type Citation struct {
	Index    int     `json:"index"`
	KBID     int     `json:"kb_id"`
	KBName   string  `json:"kb_name"`
	FileID   int     `json:"file_id"`
	FileName string  `json:"file_name"`
	Seq      int     `json:"seq"`
	Start    int     `json:"start"`
	End      int     `json:"end"`
	Heading  string  `json:"heading,omitempty"`
	Snippet  string  `json:"snippet"`
	Score    float64 `json:"score"`
	Content  string  `json:"-"` // 注入上下文的全文，不下发
}

// Result 一次检索的结果与统计。
type Result struct {
	Citations    []Citation
	AllowedFiles int
	Visible      int // 可见 chunk 数
	Strategy     string
	VectorDur    time.Duration
	Total        time.Duration
}

// Retriever 检索服务（D-14 / D-27）：先按 ACL 算出可见文件，再在同一条 SQL 中预过滤检索。
type Retriever struct {
	DB    *ent.Client
	AZ    *authz.Service
	Store ChunkStore
	GW    *gateway.Service
	Audit audit.Writer
	// Embed 非空时代替网关嵌入问题（仅供性能复测工具 cmd/kbbench 使用，不计量）。
	Embed func(ctx context.Context, model, q string) ([]float32, error)
}

// Retrieve kbIDs 为空表示全部可见知识库。审计写失败返回 ErrRetrieveAudit。
func (r *Retriever) Retrieve(ctx context.Context, p *auth.Principal, kbIDs []int, query, requestID string, k int) (*Result, error) {
	t0 := time.Now()
	if k <= 0 {
		k = DefaultTopK
	}
	a, err := LoadAccess(ctx, r.AZ, p)
	if err != nil {
		return nil, err
	}
	ids, err := AllowedFiles(ctx, r.DB, a, p.OrgID, kbIDs)
	if err != nil {
		return nil, err
	}
	res := &Result{AllowedFiles: len(ids), Strategy: "none"}
	var fails []string
	if len(ids) > 0 {
		files, err := loadFiles(ctx, r.DB, ids)
		if err != nil {
			return nil, err
		}
		kbs, err := r.DB.KnowledgeBase.Query().Where(knowledgebase.IDIn(kbIDsOf(files)...)).All(ctx)
		if err != nil {
			return nil, err
		}
		kbName, kbModel := map[int]string{}, map[int]string{}
		for _, b := range kbs {
			kbName[b.ID], kbModel[b.ID] = b.Name, b.EmbeddingModel
		}
		fileName := map[int]string{}
		// 按文件实际建索引所用的 embedding 模型分组（不同模型的向量不可比）。换模型重建期间，
		// 尚未重建的文件仍按旧模型检索（待确认 2「重建期间旧索引继续可用」）。
		groups := map[string]*SearchReq{}
		for _, f := range files {
			fileName[f.ID] = f.Name
			m := f.EmbeddingModel
			if m == "" {
				m = kbModel[f.KBID]
			}
			g := groups[m]
			if g == nil {
				g = &SearchReq{Text: query}
				groups[m] = g
			}
			g.FileIDs = append(g.FileIDs, f.ID)
			g.VisibleChunks += f.ChunkCount
			res.Visible += f.ChunkCount
		}
		var lists [][]Hit
		ectx := egress.WithCaller(ctx, egress.Caller{Module: "kb.retrieve", UserID: p.ID, Email: p.Email})
		for model, g := range groups {
			vec, err := r.embedQuery(ectx, p, model, query, requestID)
			if err != nil {
				fails = append(fails, model)
				continue
			}
			g.Vec = vec
			hits, st, err := Hybrid(ctx, r.Store, *g, k)
			if err != nil {
				return nil, err
			}
			res.VectorDur += st.VectorDur
			if res.Strategy == "none" || st.Strategy != StrategyExact {
				res.Strategy = st.Strategy
			}
			lists = append(lists, hits)
		}
		var hits []Hit
		if len(lists) == 1 {
			hits = lists[0]
		} else {
			hits = RRF(k, lists...)
		}
		for i, h := range hits {
			res.Citations = append(res.Citations, Citation{Index: i + 1, KBID: h.KBID, KBName: kbName[h.KBID], FileID: h.FileID,
				FileName: fileName[h.FileID], Seq: h.Seq, Start: h.Start, End: h.End, Heading: h.Heading,
				Snippet: truncRunes(h.Content, 200), Score: h.Score, Content: h.Content})
		}
	}
	res.Total = time.Since(t0)
	hitsLog := make([]map[string]int, 0, len(res.Citations))
	for _, c := range res.Citations {
		hitsLog = append(hitsLog, map[string]int{"file_id": c.FileID, "seq": c.Seq, "start": c.Start, "end": c.End})
	}
	ks := make([]string, 0, len(kbIDs))
	for _, id := range kbIDs {
		ks = append(ks, strconv.Itoa(id))
	}
	// AC-04：检索日志只记 ID、位置、策略和耗时，不记问题原文与片段内容。
	if err := r.Audit.Write(ctx, audit.Entry{OrgID: p.OrgID, ActorID: p.ID, ActorEmail: p.Email, Action: "kb.retrieve",
		ResourceType: "kb", Phase: "result", Outcome: "success", RequestID: requestID, Detail: map[string]any{
			"kb_ids": ks, "allowed_files": res.AllowedFiles, "visible_chunks": res.Visible, "hits": hitsLog,
			"strategy": res.Strategy, "vector_ms": res.VectorDur.Milliseconds(), "total_ms": res.Total.Milliseconds(),
			"embed_failed_models": fails,
		}}); err != nil {
		return nil, ErrRetrieveAudit
	}
	if len(fails) > 0 && len(res.Citations) == 0 {
		return res, fmt.Errorf("知识库检索失败：embedding 模型 %v 不可用", fails)
	}
	return res, nil
}

func kbIDsOf(fs []*ent.KBFile) []int {
	seen := map[int]bool{}
	var out []int
	for _, f := range fs {
		if !seen[f.KBID] {
			seen[f.KBID] = true
			out = append(out, f.KBID)
		}
	}
	return out
}

func (r *Retriever) embedQuery(ctx context.Context, p *auth.Principal, model, q, requestID string) ([]float32, error) {
	if r.Embed != nil {
		return r.Embed(ctx, model, q)
	}
	start := time.Now()
	vec, u, m, err := EmbedQuery(ctx, r.GW, model, q)
	if m != nil {
		rec := usage.Record{OrgID: p.OrgID, UserID: p.ID, DeptID: usage.PrimaryDept(ctx, r.DB, p.ID), Model: m, ProviderID: m.ProviderID,
			RequestID: truncRunes("kbq-"+requestID+"-"+model, usageRequestIDLimit), Purpose: "embedding", Latency: time.Since(start)}
		if u != nil {
			rec.Usage = *u
		}
		if err != nil {
			rec.ErrorCode = ErrCodeEmbed
		}
		_, _ = usage.Write(context.WithoutCancel(ctx), r.DB, rec)
	}
	return vec, err
}

// fileBatch 单条 SQL 的 IN 参数上限（PostgreSQL 扩展协议最多 65535 个参数）。
const fileBatch = 20000

// loadFiles 分批读取可见文件的元数据；可见文件可能有十万级（PR4 复测发现）。
func loadFiles(ctx context.Context, db *ent.Client, ids []int) ([]*ent.KBFile, error) {
	var out []*ent.KBFile
	for i := 0; i < len(ids); i += fileBatch {
		part, err := db.KBFile.Query().Where(kbfile.IDIn(ids[i:min(i+fileBatch, len(ids))]...)).
			Select(kbfile.FieldID, kbfile.FieldKBID, kbfile.FieldName, kbfile.FieldChunkCount, kbfile.FieldEmbeddingModel).All(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	return out, nil
}
