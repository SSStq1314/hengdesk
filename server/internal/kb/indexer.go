package kb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SSStq1314/hengdesk/server/internal/docparse"
	"github.com/SSStq1314/hengdesk/server/internal/egress"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/kbfile"
	"github.com/SSStq1314/hengdesk/server/internal/ent/knowledgebase"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/usage"
)

// Objects 对象存储（与 api.FileStore 同形）。
type Objects interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// 失败原因码（kb_files.error_code），前端按码展示；error 字段是给人看的中文说明。
const (
	ErrCodeUnsupported  = "unsupported"   // 格式不支持 / 内容与扩展名不符 / 含宏
	ErrCodeNoText       = "no_text"       // 没有可抽取的文本（扫描件 PDF，OCR 规划中）
	ErrCodeParse        = "parse_failed"  // 解析失败（文件损坏或加密）
	ErrCodeTooLarge     = "too_large"     // 抽取文本超过 MaxDocRunes
	ErrCodeModel        = "model_missing" // 知识库的 embedding 模型不存在或已停用
	ErrCodeEmbed        = "embed_failed"  // 调用 embedding 失败（可重试）
	ErrCodeDimMismatch  = "dim_mismatch"  // embedding 维度不是 1024
	ErrCodeStorage      = "storage"       // 读取原件 / 写入索引失败（可重试）
	embedBatch          = 32
	maxEmbedInputRunes  = 8000
	usageRequestIDLimit = 64
)

// Indexer 解析 → 切片 → embedding → 写入 chunk 存储。由 worker 的 kb:index 任务调用（测试中同步调用）。
type Indexer struct {
	DB      *ent.Client
	Objects Objects
	Store   ChunkStore
	GW      *gateway.Service
	Parser  *docparse.Extractor
	Log     *slog.Logger
}

type failure struct{ code, msg string }

func (f *failure) Error() string { return f.code + ": " + f.msg }

func fail(code, format string, a ...any) *failure {
	return &failure{code: code, msg: fmt.Sprintf(format, a...)}
}

// Index 处理一个文件。返回 error 仅表示「值得让队列重试」的系统性错误（数据库不可用）；
// 文件本身的失败写进 kb_files.status=failed 后返回 nil。
func (x *Indexer) Index(ctx context.Context, fileID int) error {
	f, err := x.DB.KBFile.Get(ctx, fileID)
	if ent.IsNotFound(err) {
		return nil // 文件已删除
	}
	if err != nil {
		return err
	}
	if f.Status != kbfile.StatusPending {
		return nil // 重复投递或已在处理
	}
	kbase, err := x.DB.KnowledgeBase.Query().Where(knowledgebase.ID(f.KBID), knowledgebase.DeletedAtIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	n, err := x.DB.KBFile.Update().Where(kbfile.ID(f.ID), kbfile.StatusEQ(kbfile.StatusPending)).
		SetStatus(kbfile.StatusParsing).AddAttempts(1).SetErrorCode("").SetError("").Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil // 被其他 worker 抢先
	}
	chunks, chars, ferr := x.build(ctx, f, kbase)
	if ferr == nil {
		ferr = x.store(ctx, f, chunks)
	}
	if ferr != nil {
		var fe *failure
		if !errors.As(ferr, &fe) {
			fe = fail(ErrCodeStorage, "索引写入失败，请重试")
			x.log().Error("kb index failed", "file", f.ID, "err", ferr)
		}
		_, err := x.DB.KBFile.Update().Where(kbfile.ID(f.ID)).SetStatus(kbfile.StatusFailed).
			SetErrorCode(fe.code).SetError(truncRunes(fe.msg, 500)).SetChunkCount(0).Save(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return err
		}
		_ = x.Store.DeleteFile(context.WithoutCancel(ctx), f.ID)
		return nil
	}
	now := time.Now()
	n, err = x.DB.KBFile.Update().Where(kbfile.ID(f.ID), kbfile.StatusEQ(kbfile.StatusIndexing)).
		SetStatus(kbfile.StatusReady).SetChars(chars).SetChunkCount(len(chunks)).
		SetEmbeddingModel(kbase.EmbeddingModel).SetIndexedAt(now).Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		// 处理期间文件被删除或被重新排队（如换了 embedding 模型）：本次结果作废
		if _, gerr := x.DB.KBFile.Get(ctx, f.ID); ent.IsNotFound(gerr) {
			_ = x.Store.DeleteFile(context.WithoutCancel(ctx), f.ID)
		}
	}
	return nil
}

func (x *Indexer) log() *slog.Logger {
	if x.Log != nil {
		return x.Log
	}
	return slog.Default()
}

func (x *Indexer) build(ctx context.Context, f *ent.KBFile, kbase *ent.KnowledgeBase) ([]Chunk, int, error) {
	rc, err := x.Objects.Get(ctx, f.ObjectKey)
	if err != nil {
		return nil, 0, fail(ErrCodeStorage, "读取原文件失败，请重试")
	}
	data, err := io.ReadAll(io.LimitReader(rc, f.Size+1))
	_ = rc.Close()
	if err != nil {
		return nil, 0, fail(ErrCodeStorage, "读取原文件失败，请重试")
	}
	doc, err := x.Parser.Parse(ctx, f.Kind, data)
	switch {
	case errors.Is(err, docparse.ErrEmpty):
		return nil, 0, fail(ErrCodeNoText, "没有识别到文字。扫描件或图片型 PDF 需要 OCR，暂不支持（规划中）")
	case err != nil:
		return nil, 0, fail(ErrCodeParse, "文件解析失败，可能已损坏或加密")
	}
	text := doc.PlainText()
	chars := utf8.RuneCountInString(text)
	if chars > MaxDocRunes {
		return nil, 0, fail(ErrCodeTooLarge, "文件文字超过 %d 万字，请拆分后上传", MaxDocRunes/10000)
	}
	pieces := Split(doc, kbase.ChunkSize, kbase.ChunkOverlap)
	if len(pieces) == 0 {
		return nil, 0, fail(ErrCodeNoText, "没有识别到文字。扫描件或图片型 PDF 需要 OCR，暂不支持（规划中）")
	}
	if _, err := x.DB.KBFile.Update().Where(kbfile.ID(f.ID)).SetStatus(kbfile.StatusIndexing).SetChars(chars).Save(ctx); err != nil {
		return nil, 0, err
	}
	vecs, ferr := x.embed(ctx, f, kbase.EmbeddingModel, pieces)
	if ferr != nil {
		return nil, 0, ferr
	}
	out := make([]Chunk, len(pieces))
	for i, p := range pieces {
		out[i] = Chunk{OrgID: f.OrgID, KBID: f.KBID, FileID: f.ID, Piece: p, Embedding: vecs[i]}
	}
	return out, chars, nil
}

// EmbedInput 送去 embedding 的文本：标题作为上下文前缀。
func EmbedInput(p Piece) string {
	s := p.Content
	if p.Heading != "" {
		s = p.Heading + "\n" + s
	}
	return truncRunes(s, maxEmbedInputRunes)
}

func (x *Indexer) embed(ctx context.Context, f *ent.KBFile, model string, pieces []Piece) ([][]float32, error) {
	ctx = egress.WithCaller(ctx, egress.Caller{Module: "kb.index", UserID: f.UploadedBy})
	tgt, gerr := x.GW.Resolve(ctx, model)
	if gerr != nil {
		return nil, fail(ErrCodeModel, "知识库的 embedding 模型「%s」不存在或已停用，请管理员在知识库设置中更换", model)
	}
	start := time.Now()
	var total gateway.Usage
	out := make([][]float32, 0, len(pieces))
	var embErr error
	for i := 0; i < len(pieces) && embErr == nil; i += embedBatch {
		part := pieces[i:min(i+embedBatch, len(pieces))]
		in := make([]string, len(part))
		for j, p := range part {
			in[j] = EmbedInput(p)
		}
		res, ge := x.GW.Client.Embed(ctx, tgt.Upstream, tgt.Model.UpstreamModel, in)
		if ge != nil {
			embErr = fail(ErrCodeEmbed, "调用 embedding 模型失败：%s", ge.Message)
			break
		}
		for _, v := range res.Vectors {
			if len(v) != Dim {
				embErr = fail(ErrCodeDimMismatch, "embedding 模型返回 %d 维向量，知识库要求 %d 维，请更换模型", len(v), Dim)
				break
			}
		}
		out = append(out, res.Vectors...)
		total.PromptTokens += res.Usage.PromptTokens
		total.TotalTokens += res.Usage.TotalTokens
		total.Estimated = total.Estimated || res.Usage.Estimated
	}
	// 用量记账：计到上传者名下（不做预算拦截，见 m1-block4.md 待确认）
	rec := usage.Record{OrgID: f.OrgID, UserID: f.UploadedBy, DeptID: usage.PrimaryDept(ctx, x.DB, f.UploadedBy),
		Model: tgt.Model, ProviderID: tgt.Model.ProviderID, RequestID: truncRunes(fmt.Sprintf("kbfile-%d-%d", f.ID, f.Attempts+1), usageRequestIDLimit),
		Purpose: "embedding", Usage: total, Latency: time.Since(start)}
	if embErr != nil {
		var fe *failure
		if errors.As(embErr, &fe) {
			rec.ErrorCode = fe.code
		}
	}
	if total.TotalTokens > 0 || embErr != nil {
		if _, err := usage.Write(context.WithoutCancel(ctx), x.DB, rec); err != nil {
			x.log().Error("embedding usage write failed", "file", f.ID, "err", err)
		}
	}
	if embErr != nil {
		return nil, embErr
	}
	return out, nil
}

func (x *Indexer) store(ctx context.Context, f *ent.KBFile, chunks []Chunk) error {
	if err := x.Store.Replace(ctx, f.ID, chunks); err != nil {
		if strings.Contains(err.Error(), "dimension") {
			return fail(ErrCodeDimMismatch, "向量维度与索引不符")
		}
		return err
	}
	return nil
}

// EmbedQuery 检索时对问题做 embedding（PR2 使用）。
func EmbedQuery(ctx context.Context, gw *gateway.Service, model, q string) ([]float32, *gateway.Usage, *ent.Model, error) {
	tgt, gerr := gw.Resolve(ctx, model)
	if gerr != nil {
		return nil, nil, nil, gerr
	}
	res, ge := gw.Client.Embed(ctx, tgt.Upstream, tgt.Model.UpstreamModel, []string{truncRunes(q, maxEmbedInputRunes)})
	if ge != nil {
		return nil, nil, tgt.Model, ge
	}
	if len(res.Vectors) != 1 || len(res.Vectors[0]) != Dim {
		return nil, nil, tgt.Model, fail(ErrCodeDimMismatch, "embedding 维度不是 %d", Dim)
	}
	return res.Vectors[0], &res.Usage, tgt.Model, nil
}
