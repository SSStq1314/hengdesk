package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/docparse"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/acl"
	"github.com/SSStq1314/hengdesk/server/internal/ent/deptclosure"
	"github.com/SSStq1314/hengdesk/server/internal/ent/kbfile"
	"github.com/SSStq1314/hengdesk/server/internal/ent/knowledgebase"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/kb"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// 知识库（M1 第四块 PR1，FR-16/17）。
// 授权：全部走 acl 表（resource_type=kb / kb_file），见 kb.Access。没有 view 的知识库一律 404（与不存在无法区分）；
// 有 view 但缺少所需权限位同样 404 并记 authz.denied。写操作先写 attempt 审计（fail-closed）。
// 解析与索引异步进行（kb:index 任务），上传接口只做格式校验、去重、落对象存储。

type kbHandlers struct {
	db     *ent.Client
	az     *authz.Service
	gw     *gateway.Service
	fs     FileStore
	store  kb.ChunkStore
	enq    kb.Enqueuer
	maxB   int64
	log    *slog.Logger
	now    func() time.Time
	detect func(name string, data []byte) (string, error)
	auth   *auth.Service
}

type kbView struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	EmbeddingModel string `json:"embedding_model"`
	ChunkSize      int    `json:"chunk_size"`
	ChunkOverlap   int    `json:"chunk_overlap"`
	// StrictGrounding「仅依据资料回答」（D-42）。
	StrictGrounding bool      `json:"strict_grounding"`
	Perms           []string  `json:"perms"`
	FileCount       int       `json:"file_count"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type kbFileView struct {
	ID             int        `json:"id"`
	KBID           int        `json:"kb_id"`
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	Size           int64      `json:"size"`
	SHA256         string     `json:"sha256"`
	Status         string     `json:"status"`
	ErrorCode      string     `json:"error_code,omitempty"`
	Error          string     `json:"error,omitempty"`
	Chars          int        `json:"chars"`
	ChunkCount     int        `json:"chunk_count"`
	ACLCustom      bool       `json:"acl_custom"`
	EmbeddingModel string     `json:"embedding_model,omitempty"`
	Attempts       int        `json:"attempts"`
	IndexedAt      *time.Time `json:"indexed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

func toKBFile(f *ent.KBFile) kbFileView {
	return kbFileView{ID: f.ID, KBID: f.KBID, Name: f.Name, Kind: f.Kind, Size: f.Size, SHA256: f.Sha256, Status: string(f.Status),
		ErrorCode: f.ErrorCode, Error: f.Error, Chars: f.Chars, ChunkCount: f.ChunkCount, ACLCustom: f.ACLCustom,
		EmbeddingModel: f.EmbeddingModel, Attempts: f.Attempts, IndexedAt: f.IndexedAt, CreatedAt: f.CreatedAt}
}

func permNames(bits int) []string {
	out := []string{}
	for _, n := range []string{"view", "use", "edit", "manage"} {
		if bits&rbac.BitNames[n] != 0 {
			out = append(out, n)
		}
	}
	return out
}

func (h *kbHandlers) access(c echo.Context) (*auth.Principal, *kb.Access, error) {
	p, err := principal(c)
	if err != nil {
		return nil, nil, err
	}
	a, err := kb.LoadAccess(c.Request().Context(), h.az, p)
	return p, a, err
}

// kbOr404 取本组织未删除、且本人拥有 bit 的知识库；否则 404（记 authz.denied）。
func (h *kbHandlers) kbOr404(c echo.Context, bit int) (*auth.Principal, *kb.Access, *ent.KnowledgeBase, error) {
	p, a, err := h.access(c)
	if err != nil {
		return nil, nil, nil, err
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return nil, nil, nil, authz.Deny(c, "kb:bad_id")
	}
	if !a.Has(id, bit) {
		return nil, nil, nil, authz.Deny(c, "kb:acl")
	}
	k, err := h.db.KnowledgeBase.Query().Where(knowledgebase.ID(id), knowledgebase.OrgID(p.OrgID), knowledgebase.DeletedAtIsNil()).
		Only(c.Request().Context())
	if ent.IsNotFound(err) {
		return nil, nil, nil, authz.Deny(c, "kb:missing")
	}
	return p, a, k, err
}

// fileOr404 取知识库下本人可见的文件；否则 404。
func (h *kbHandlers) fileOr404(c echo.Context, k *ent.KnowledgeBase, a *kb.Access) (*ent.KBFile, error) {
	fid, err := strconv.Atoi(c.Param("fid"))
	if err != nil || fid <= 0 {
		return nil, authz.Deny(c, "kb_file:bad_id")
	}
	f, err := h.db.KBFile.Query().Where(kbfile.ID(fid), kbfile.KBID(k.ID)).Only(c.Request().Context())
	if ent.IsNotFound(err) || (err == nil && !a.CanSeeFile(f)) {
		return nil, authz.Deny(c, "kb_file:acl")
	}
	return f, err
}

func (h *kbHandlers) view(ctx context.Context, k *ent.KnowledgeBase, a *kb.Access) kbView {
	n, _ := h.db.KBFile.Query().Where(kbfile.KBID(k.ID)).Count(ctx)
	return kbView{ID: k.ID, Name: k.Name, Description: k.Description, EmbeddingModel: k.EmbeddingModel, ChunkSize: k.ChunkSize,
		ChunkOverlap: k.ChunkOverlap, StrictGrounding: k.StrictGrounding, Perms: permNames(a.KB[k.ID]), FileCount: n, CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt}
}

func (h *kbHandlers) record(c echo.Context, action, phase, outcome, resType string, id int, detail map[string]any) error {
	return audit.Record(c, audit.Entry{Action: action, Phase: phase, Outcome: outcome, ResourceType: resType,
		ResourceID: strconv.Itoa(id), Detail: detail})
}

// list GET /kbs：本人有 view 的知识库。
func (h *kbHandlers) list(c echo.Context) error {
	p, a, err := h.access(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	ids := a.KBIDs(rbac.BitView)
	out := []kbView{}
	if len(ids) > 0 {
		ks, err := h.db.KnowledgeBase.Query().Where(knowledgebase.OrgID(p.OrgID), knowledgebase.IDIn(ids...),
			knowledgebase.DeletedAtIsNil()).Order(ent.Desc(knowledgebase.FieldUpdatedAt), ent.Desc(knowledgebase.FieldID)).All(ctx)
		if err != nil {
			return err
		}
		for _, k := range ks {
			out = append(out, h.view(ctx, k, a))
		}
	}
	roots, orgWide := p.Perms.DeptRoots(rbac.KBCreate)
	return c.JSON(http.StatusOK, map[string]any{"items": out, "can_create": orgWide || len(roots) > 0})
}

type kbInput struct {
	Name            *string `json:"name"`
	Description     *string `json:"description"`
	EmbeddingModel  *string `json:"embedding_model"`
	ChunkSize       *int    `json:"chunk_size"`
	ChunkOverlap    *int    `json:"chunk_overlap"`
	StrictGrounding *bool   `json:"strict_grounding"`
}

func (h *kbHandlers) checkModel(ctx context.Context, name string) error {
	if _, ge := h.gw.Resolve(ctx, name); ge != nil {
		return bad("embedding 模型「" + name + "」不存在或已停用")
	}
	return nil
}

// create POST /kbs（需要 kb.create）：创建者获得 manage；部门管理员创建时默认共享给其管辖部门（D-33）。
func (h *kbHandlers) create(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	var in kbInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	ctx := c.Request().Context()
	name := ""
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if name == "" || len([]rune(name)) > 100 {
		return bad("名称必填，最多 100 字")
	}
	desc := ""
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
	}
	if len([]rune(desc)) > 500 {
		return bad("描述最多 500 字")
	}
	if in.EmbeddingModel == nil || strings.TrimSpace(*in.EmbeddingModel) == "" {
		return bad("请选择 embedding 模型")
	}
	model := strings.TrimSpace(*in.EmbeddingModel)
	if err := h.checkModel(ctx, model); err != nil {
		return err
	}
	size, overlap := kb.DefaultChunkSize, kb.DefaultChunkOverlap
	if in.ChunkSize != nil {
		size = *in.ChunkSize
	}
	if in.ChunkOverlap != nil {
		overlap = *in.ChunkOverlap
	}
	if !kb.ValidChunking(size, overlap) {
		return bad(fmt.Sprintf("切片长度需在 %d–%d 字之间，重叠需小于切片长度的一半", kb.MinChunkSize, kb.MaxChunkSize))
	}
	strict := in.StrictGrounding != nil && *in.StrictGrounding
	detail := map[string]any{"name": name, "embedding_model": model, "chunk_size": size, "chunk_overlap": overlap, "strict_grounding": strict}
	if err := audit.Record(c, audit.Entry{Action: "kb.create", Phase: "attempt", ResourceType: "kb", Detail: detail}); err != nil {
		return err
	}
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	k, err := tx.KnowledgeBase.Create().SetOrgID(p.OrgID).SetName(name).SetDescription(desc).SetEmbeddingModel(model).
		SetChunkSize(size).SetChunkOverlap(overlap).SetStrictGrounding(strict).SetCreatedBy(p.ID).Save(ctx)
	if err != nil {
		return err
	}
	if err := (&authz.Service{DB: tx.Client()}).InitShare(ctx, p, rbac.ResKB, strconv.Itoa(k.ID)); err != nil {
		if errors.Is(err, authz.ErrOutsideSubtree) {
			return authz.Deny(c, "kb:create_scope")
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	detail["kb_id"] = k.ID
	if err := h.record(c, "kb.create", "result", "ok", "kb", k.ID, detail); err != nil {
		h.log.Error("kb create result audit failed", "kb", k.ID)
	}
	a, err := kb.LoadAccess(ctx, h.az, p)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, h.view(ctx, k, a))
}

func (h *kbHandlers) get(c echo.Context) error {
	_, a, k, err := h.kbOr404(c, rbac.BitView)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, h.view(c.Request().Context(), k, a))
}

// update PATCH /kbs/:id（manage）。换 embedding 模型或切片参数会把全部文件重新排队；
// 重建期间旧切片仍可检索，单个文件重建完成时原子替换（kb.ChunkStore.Replace）。
func (h *kbHandlers) update(c echo.Context) error {
	_, a, k, err := h.kbOr404(c, rbac.BitManage)
	if err != nil {
		return err
	}
	var in kbInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	ctx := c.Request().Context()
	u := k.Update()
	detail := map[string]any{"name": k.Name}
	reindex := false
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len([]rune(n)) > 100 {
			return bad("名称必填，最多 100 字")
		}
		u.SetName(n)
		detail["new_name"] = n
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if len([]rune(d)) > 500 {
			return bad("描述最多 500 字")
		}
		u.SetDescription(d)
	}
	if in.EmbeddingModel != nil && strings.TrimSpace(*in.EmbeddingModel) != k.EmbeddingModel {
		m := strings.TrimSpace(*in.EmbeddingModel)
		if err := h.checkModel(ctx, m); err != nil {
			return err
		}
		u.SetEmbeddingModel(m)
		detail["embedding_model"], detail["old_embedding_model"] = m, k.EmbeddingModel
		reindex = true
	}
	size, overlap := k.ChunkSize, k.ChunkOverlap
	if in.ChunkSize != nil {
		size = *in.ChunkSize
	}
	if in.ChunkOverlap != nil {
		overlap = *in.ChunkOverlap
	}
	if size != k.ChunkSize || overlap != k.ChunkOverlap {
		if !kb.ValidChunking(size, overlap) {
			return bad(fmt.Sprintf("切片长度需在 %d–%d 字之间，重叠需小于切片长度的一半", kb.MinChunkSize, kb.MaxChunkSize))
		}
		u.SetChunkSize(size).SetChunkOverlap(overlap)
		detail["chunk_size"], detail["chunk_overlap"] = size, overlap
		reindex = true
	}
	if in.StrictGrounding != nil && *in.StrictGrounding != k.StrictGrounding {
		u.SetStrictGrounding(*in.StrictGrounding)
		detail["strict_grounding"], detail["old_strict_grounding"] = *in.StrictGrounding, k.StrictGrounding
	}
	detail["reindex"] = reindex
	if err := h.record(c, "kb.update", "attempt", "", "kb", k.ID, detail); err != nil {
		return err
	}
	k, err = u.Save(ctx)
	if err != nil {
		return err
	}
	if reindex {
		if _, err := h.requeue(ctx, h.db.KBFile.Query().Where(kbfile.KBID(k.ID), kbfile.StatusNotIn(kbfile.StatusPending))); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusOK, h.view(ctx, k, a))
}

// requeue 把文件置为 pending 并投递索引任务（投递失败由 kb:sweep 补偿）。
func (h *kbHandlers) requeue(ctx context.Context, q *ent.KBFileQuery) (int, error) {
	ids, err := q.IDs(ctx)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	if _, err := h.db.KBFile.Update().Where(kbfile.IDIn(ids...)).SetStatus(kbfile.StatusPending).
		SetErrorCode("").SetError("").Save(ctx); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := h.enq.Enqueue(ctx, id); err != nil {
			h.log.Warn("kb index enqueue failed; sweep will retry", "file", id, "err", err)
		}
	}
	return len(ids), nil
}

// remove DELETE /kbs/:id（manage）：知识库软删除；文件、切片、原件、ACL 硬删除（待确认 10）。
func (h *kbHandlers) remove(c echo.Context) error {
	_, _, k, err := h.kbOr404(c, rbac.BitManage)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	fs, err := h.db.KBFile.Query().Where(kbfile.KBID(k.ID)).All(ctx)
	if err != nil {
		return err
	}
	if err := h.record(c, "kb.delete", "attempt", "", "kb", k.ID, map[string]any{"name": k.Name, "files": len(fs)}); err != nil {
		return err
	}
	if err := k.Update().SetDeletedAt(h.now()).Exec(ctx); err != nil {
		return err
	}
	bg := context.WithoutCancel(ctx)
	if err := h.store.DeleteKB(bg, k.ID); err != nil {
		h.log.Error("kb chunks delete failed", "kb", k.ID, "err", err)
	}
	fids := make([]string, 0, len(fs))
	for _, f := range fs {
		fids = append(fids, strconv.Itoa(f.ID))
		if h.fs != nil {
			if err := h.fs.Delete(bg, f.ObjectKey); err != nil {
				h.log.Error("kb object delete failed", "file", f.ID, "err", err)
			}
		}
	}
	if _, err := h.db.KBFile.Delete().Where(kbfile.KBID(k.ID)).Exec(bg); err != nil {
		return err
	}
	if _, err := h.db.ACL.Delete().Where(acl.Or(
		acl.And(acl.ResourceType(rbac.ResKB), acl.ResourceID(strconv.Itoa(k.ID))),
		acl.And(acl.ResourceType(rbac.ResKBFile), acl.ResourceIDIn(fids...)),
	)).Exec(bg); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- 文件 ----

// listFiles GET /kbs/:id/files（view）：只列出本人可见的文件。
func (h *kbHandlers) listFiles(c echo.Context) error {
	_, a, k, err := h.kbOr404(c, rbac.BitView)
	if err != nil {
		return err
	}
	fs, err := h.db.KBFile.Query().Where(kbfile.KBID(k.ID)).Order(ent.Desc(kbfile.FieldID)).All(c.Request().Context())
	if err != nil {
		return err
	}
	out := []kbFileView{}
	for _, f := range fs {
		if a.CanSeeFile(f) {
			out = append(out, toKBFile(f))
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

// uniqueName 同名不同内容时改名为「名称 (2).ext」（待确认 6）。
func uniqueName(name string, taken func(string) bool) string {
	if !taken(name) {
		return name
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		n := truncRunes(base, 255-len([]rune(ext))-8) + " (" + strconv.Itoa(i) + ")" + ext
		if !taken(n) {
			return n
		}
	}
	return ""
}

// upload POST /kbs/:id/files（edit，multipart 字段 file）。返回 201 新文件；内容重复时返回 200 + duplicate=true 与已有文件。
func (h *kbHandlers) upload(c echo.Context) error {
	p, a, k, err := h.kbOr404(c, rbac.BitEdit)
	if err != nil {
		return err
	}
	if h.fs == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "对象存储未配置，暂不能上传文件")
	}
	ctx := c.Request().Context()
	tooBig := fileErr(http.StatusRequestEntityTooLarge, "文件超过大小上限（"+strconv.FormatInt(h.maxB>>20, 10)+" MB）")
	r := c.Request()
	r.Body = http.MaxBytesReader(c.Response(), r.Body, h.maxB+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		return bad("请用 multipart/form-data 上传，字段名 file")
	}
	var name string
	var data []byte
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return tooBig
			}
			return bad("上传数据格式错误")
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		name = cleanFileName(part.FileName())
		data, err = io.ReadAll(io.LimitReader(part, h.maxB+1))
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return tooBig
			}
			return bad("读取上传文件失败")
		}
		break
	}
	switch {
	case data == nil:
		return bad("缺少文件（字段名 file）")
	case int64(len(data)) > h.maxB:
		return tooBig
	case name == "":
		return bad("文件名无效")
	case len(data) == 0:
		return bad("文件为空")
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	detail := map[string]any{"kb_id": k.ID, "name": name, "size": len(data), "sha256": hash}
	kind, derr := h.detect(name, data)
	if derr != nil {
		detail["reason"] = derr.Error()
		_ = h.record(c, "kb.file.upload", "result", "rejected", "kb", k.ID, detail)
		switch {
		case errors.Is(derr, docparse.ErrUnsupported):
			return fileErr(http.StatusUnsupportedMediaType, "不支持的文件类型。支持：PDF、Word（.docx）、Excel（.xlsx）、PowerPoint（.pptx）、TXT、Markdown")
		case errors.Is(derr, docparse.ErrMismatch):
			return fileErr(http.StatusUnsupportedMediaType, "文件内容与扩展名不符，已拒绝（类型按文件内容判定）")
		case errors.Is(derr, docparse.ErrMacro):
			return fileErr(http.StatusUnprocessableEntity, "文件包含宏，已拒绝")
		default:
			return fileErr(http.StatusUnprocessableEntity, "文件结构异常（压缩比或条目数超限），已拒绝")
		}
	}
	detail["kind"] = kind
	// 同一知识库内容去重（待确认 6）：返回已有文件并提示，不重复索引。
	if dup, err := h.db.KBFile.Query().Where(kbfile.KBID(k.ID), kbfile.Sha256(hash)).Only(ctx); err == nil {
		detail["duplicate_of"] = dup.ID
		_ = h.record(c, "kb.file.upload", "result", "duplicate", "kb_file", dup.ID, detail)
		v := map[string]any{"duplicate": true, "message": "知识库中已有相同内容的文件「" + dup.Name + "」，未重复上传"}
		if a.CanSeeFile(dup) {
			v["file"] = toKBFile(dup)
		}
		return c.JSON(http.StatusOK, v)
	} else if !ent.IsNotFound(err) {
		return err
	}
	names, err := h.db.KBFile.Query().Where(kbfile.KBID(k.ID)).Select(kbfile.FieldName).Strings(ctx)
	if err != nil {
		return err
	}
	taken := map[string]bool{}
	for _, n := range names {
		taken[n] = true
	}
	final := uniqueName(name, func(s string) bool { return taken[s] })
	if final == "" {
		return fileErr(http.StatusConflict, "同名文件过多，请改名后上传")
	}
	if final != name {
		detail["renamed_to"] = final
	}
	if err := h.record(c, "kb.file.upload", "attempt", "", "kb", k.ID, detail); err != nil {
		return err // fail-closed
	}
	var rnd [12]byte
	_, _ = rand.Read(rnd[:])
	key := "kb/" + strconv.Itoa(p.OrgID) + "/" + strconv.Itoa(k.ID) + "/" + hex.EncodeToString(rnd[:])
	if err := h.fs.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "application/octet-stream"); err != nil {
		h.log.Error("kb file put failed", "kb", k.ID, "err", err)
		detail["reason"] = "store_failed"
		_ = h.record(c, "kb.file.upload", "result", "error", "kb", k.ID, detail)
		return echo.NewHTTPError(http.StatusServiceUnavailable, "对象存储写入失败，请稍后重试")
	}
	f, err := h.db.KBFile.Create().SetOrgID(p.OrgID).SetKBID(k.ID).SetName(final).SetKind(kind).SetSize(int64(len(data))).
		SetSha256(hash).SetObjectKey(key).SetUploadedBy(p.ID).Save(ctx)
	if err != nil {
		_ = h.fs.Delete(context.WithoutCancel(ctx), key)
		if ent.IsConstraintError(err) {
			return fileErr(http.StatusConflict, "同名或相同内容的文件刚刚被上传，请刷新后重试")
		}
		return err
	}
	detail["file_id"] = f.ID
	if err := h.record(c, "kb.file.upload", "result", "ok", "kb_file", f.ID, detail); err != nil {
		h.log.Error("kb upload result audit failed", "file", f.ID)
	}
	if err := h.enq.Enqueue(ctx, f.ID); err != nil {
		h.log.Warn("kb index enqueue failed; sweep will retry", "file", f.ID, "err", err)
	}
	if nf, err := h.db.KBFile.Get(ctx, f.ID); err == nil {
		f = nf // 同步执行（测试 / 无队列）时返回最新状态
	}
	out := map[string]any{"duplicate": false, "file": toKBFile(f)}
	if final != name {
		out["message"] = "已有同名文件，新文件已命名为「" + final + "」"
	}
	return c.JSON(http.StatusCreated, out)
}

func kbFileDetail(f *ent.KBFile) map[string]any {
	return map[string]any{"kb_id": f.KBID, "name": f.Name, "size": f.Size, "sha256": f.Sha256, "kind": f.Kind}
}

// removeFile DELETE /kbs/:id/files/:fid（edit）：删文件即删切片、原件与文件 ACL。
func (h *kbHandlers) removeFile(c echo.Context) error {
	_, a, k, err := h.kbOr404(c, rbac.BitEdit)
	if err != nil {
		return err
	}
	f, err := h.fileOr404(c, k, a)
	if err != nil {
		return err
	}
	if err := h.record(c, "kb.file.delete", "attempt", "", "kb_file", f.ID, kbFileDetail(f)); err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.db.KBFile.DeleteOneID(f.ID).Exec(ctx); err != nil {
		return notFoundOr(err)
	}
	bg := context.WithoutCancel(ctx)
	if err := h.store.DeleteFile(bg, f.ID); err != nil {
		h.log.Error("kb chunks delete failed", "file", f.ID, "err", err)
	}
	if h.fs != nil {
		if err := h.fs.Delete(bg, f.ObjectKey); err != nil {
			h.log.Error("kb object delete failed", "file", f.ID, "err", err)
		}
	}
	if _, err := h.db.ACL.Delete().Where(acl.ResourceType(rbac.ResKBFile), acl.ResourceID(strconv.Itoa(f.ID))).Exec(bg); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// retryFile POST /kbs/:id/files/:fid/retry（edit）：仅 failed 状态可重试。
func (h *kbHandlers) retryFile(c echo.Context) error {
	_, a, k, err := h.kbOr404(c, rbac.BitEdit)
	if err != nil {
		return err
	}
	f, err := h.fileOr404(c, k, a)
	if err != nil {
		return err
	}
	if f.Status != kbfile.StatusFailed {
		return fileErr(http.StatusConflict, "只有失败的文件可以重试")
	}
	d := kbFileDetail(f)
	d["error_code"] = f.ErrorCode
	if err := h.record(c, "kb.file.retry", "attempt", "", "kb_file", f.ID, d); err != nil {
		return err
	}
	ctx := c.Request().Context()
	if _, err := h.requeue(ctx, h.db.KBFile.Query().Where(kbfile.ID(f.ID), kbfile.StatusEQ(kbfile.StatusFailed))); err != nil {
		return err
	}
	nf, err := h.db.KBFile.Get(ctx, f.ID)
	if err != nil {
		return notFoundOr(err)
	}
	return c.JSON(http.StatusOK, toKBFile(nf))
}

// ---- ACL ----

type kbACLInput struct {
	SubjectType     string `json:"subject_type"`
	SubjectID       int    `json:"subject_id"`
	IncludeChildren bool   `json:"include_children"`
	Perm            string `json:"perm"` // view / use / edit / manage
}

func (h *kbHandlers) listRows(c echo.Context, orgID int, resType, resID string) error {
	ctx := c.Request().Context()
	rows, err := h.db.ACL.Query().Where(acl.OrgID(orgID), acl.ResourceType(resType), acl.ResourceID(resID)).
		Order(ent.Asc(acl.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	names := &modelHandlers{db: h.db}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{"subject_type": string(r.SubjectType), "subject_id": r.SubjectID,
			"subject_name":     names.subjectName(ctx, orgID, string(r.SubjectType), r.SubjectID),
			"include_children": r.IncludeChildren, "perms": permNames(r.Perms)})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

func (h *kbHandlers) listACL(c echo.Context) error {
	p, _, k, err := h.kbOr404(c, rbac.BitManage)
	if err != nil {
		return err
	}
	return h.listRows(c, p.OrgID, rbac.ResKB, strconv.Itoa(k.ID))
}

// grant 写一条 ACL（bits=0 为撤销）。共享范围按 kb.create 的范围校验（部门管理员只能共享到本子树）。
// 不允许撤销自己最后的 manage，避免知识库无人可管。
func (h *kbHandlers) grant(c echo.Context, p *auth.Principal, resType, resID string, in kbACLInput, bits int, action string, detail map[string]any) error {
	ctx := c.Request().Context()
	if in.SubjectType != "user" && in.SubjectType != "dept" && in.SubjectType != "role" {
		return bad("subject_type 只能是 user / dept / role")
	}
	name := (&modelHandlers{db: h.db}).subjectName(ctx, p.OrgID, in.SubjectType, in.SubjectID)
	if name == "" && bits != 0 {
		return bad("授权对象不存在")
	}
	if resType == rbac.ResKB && rbac.Normalize(bits)&rbac.BitManage == 0 {
		others, err := h.db.ACL.Query().Where(acl.OrgID(p.OrgID), acl.ResourceType(resType), acl.ResourceID(resID),
			acl.Not(acl.And(acl.SubjectTypeEQ(acl.SubjectType(in.SubjectType)), acl.SubjectID(in.SubjectID)))).All(ctx)
		if err != nil {
			return err
		}
		left := false
		for _, o := range others {
			if o.Perms&rbac.BitManage != 0 {
				left = true
			}
		}
		if !left {
			return fileErr(http.StatusConflict, "至少需要保留一个拥有「管理」权限的授权")
		}
	}
	sub := authz.Subject{Type: in.SubjectType, ID: in.SubjectID, IncludeChildren: in.IncludeChildren}
	if bits != 0 {
		ok, err := h.az.CanShareTo(ctx, p, rbac.ResKB, sub)
		if err != nil {
			return err
		}
		if !ok {
			return authz.Deny(c, "kb:share_scope")
		}
	}
	for k, v := range map[string]any{"subject_type": in.SubjectType, "subject_id": in.SubjectID, "subject_name": name,
		"include_children": in.IncludeChildren && in.SubjectType == "dept", "perms": permNames(rbac.Normalize(bits))} {
		detail[k] = v
	}
	if err := audit.Record(c, audit.Entry{Action: action, Phase: "attempt", ResourceType: resType, ResourceID: resID, Detail: detail}); err != nil {
		return err
	}
	// D-42：超级管理员给自己授权（本人、所在部门或上级部门含下级、自己持有的角色）记高风险审计，写失败拒绝执行。
	if p.IsSuperAdmin && bits != 0 {
		self, err := h.coversSelf(ctx, p, sub)
		if err != nil {
			return err
		}
		if self {
			if h.auth == nil {
				return audit.ErrRejected
			}
			extra := map[string]any{"resource_type": resType, "resource_id": resID}
			for k, v := range detail {
				extra[k] = v
			}
			if err := h.auth.RaiseAlertDetail(c, audit.Entry{OrgID: p.OrgID, ActorID: p.ID, ActorEmail: p.Email, IP: c.RealIP(),
				UserAgent: c.Request().UserAgent()}, auth.AlertKBSelfGrant, extra); err != nil {
				return err
			}
		}
	}
	if err := h.az.Grant(ctx, p.OrgID, p.ID, resType, resID, sub, bits); err != nil {
		if errors.Is(err, authz.ErrBadSubject) {
			return bad("授权对象不合法")
		}
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// coversSelf 授权对象是否覆盖操作者本人。
func (h *kbHandlers) coversSelf(ctx context.Context, p *auth.Principal, sub authz.Subject) (bool, error) {
	switch sub.Type {
	case "user":
		return sub.ID == p.ID, nil
	case "role":
		return slices.Contains(p.RoleIDs, sub.ID), nil
	case "dept":
		if slices.Contains(p.DeptIDs, sub.ID) {
			return true, nil
		}
		if !sub.IncludeChildren || len(p.DeptIDs) == 0 {
			return false, nil
		}
		return h.db.DeptClosure.Query().Where(deptclosure.OrgID(p.OrgID), deptclosure.AncestorID(sub.ID),
			deptclosure.DescendantIDIn(p.DeptIDs...)).Exist(ctx)
	}
	return false, nil
}

func (h *kbHandlers) grantACL(c echo.Context) error {
	p, _, k, err := h.kbOr404(c, rbac.BitManage)
	if err != nil {
		return err
	}
	var in kbACLInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	bits, ok := rbac.BitNames[in.Perm]
	if !ok {
		return bad("perm 只能是 view / use / edit / manage")
	}
	return h.grant(c, p, rbac.ResKB, strconv.Itoa(k.ID), in, bits, "kb.acl.grant", map[string]any{"kb_id": k.ID, "name": k.Name})
}

func (h *kbHandlers) revokeACL(c echo.Context) error {
	p, _, k, err := h.kbOr404(c, rbac.BitManage)
	if err != nil {
		return err
	}
	sid, err := strconv.Atoi(c.Param("sid"))
	if err != nil {
		return authz.ErrNotFound
	}
	in := kbACLInput{SubjectType: c.Param("type"), SubjectID: sid}
	return h.grant(c, p, rbac.ResKB, strconv.Itoa(k.ID), in, 0, "kb.acl.revoke", map[string]any{"kb_id": k.ID, "name": k.Name})
}

// 文件级 ACL（FR-17）：acl_custom=false 时继承知识库；开启后只有文件 ACL 中有 view 的主体（及知识库管理者）可见。
// 只改元数据，不重建索引；检索时现算可见文件（D-27）。

func (h *kbHandlers) fileForManage(c echo.Context) (*auth.Principal, *ent.KnowledgeBase, *ent.KBFile, error) {
	p, a, k, err := h.kbOr404(c, rbac.BitManage)
	if err != nil {
		return nil, nil, nil, err
	}
	f, err := h.fileOr404(c, k, a)
	return p, k, f, err
}

func (h *kbHandlers) listFileACL(c echo.Context) error {
	p, _, f, err := h.fileForManage(c)
	if err != nil {
		return err
	}
	return h.listRows(c, p.OrgID, rbac.ResKBFile, strconv.Itoa(f.ID))
}

func (h *kbHandlers) grantFileACL(c echo.Context) error {
	p, _, f, err := h.fileForManage(c)
	if err != nil {
		return err
	}
	var in kbACLInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	if in.Perm != "view" {
		return bad("文件级授权只支持 view（可检索、可被引用）")
	}
	return h.grant(c, p, rbac.ResKBFile, strconv.Itoa(f.ID), in, rbac.BitView, "kb.file.acl.grant", kbFileDetail(f))
}

func (h *kbHandlers) revokeFileACL(c echo.Context) error {
	p, _, f, err := h.fileForManage(c)
	if err != nil {
		return err
	}
	sid, err := strconv.Atoi(c.Param("sid"))
	if err != nil {
		return authz.ErrNotFound
	}
	in := kbACLInput{SubjectType: c.Param("type"), SubjectID: sid}
	return h.grant(c, p, rbac.ResKBFile, strconv.Itoa(f.ID), in, 0, "kb.file.acl.revoke", kbFileDetail(f))
}

// setFileACLMode PUT /kbs/:id/files/:fid/acl-mode {"custom": bool}。
func (h *kbHandlers) setFileACLMode(c echo.Context) error {
	_, _, f, err := h.fileForManage(c)
	if err != nil {
		return err
	}
	var in struct {
		Custom *bool `json:"custom"`
	}
	if err := c.Bind(&in); err != nil || in.Custom == nil {
		return bad("请求格式错误，需要 custom: true / false")
	}
	d := kbFileDetail(f)
	d["custom"] = *in.Custom
	if err := h.record(c, "kb.file.acl.mode", "attempt", "", "kb_file", f.ID, d); err != nil {
		return err
	}
	nf, err := f.Update().SetACLCustom(*in.Custom).Save(c.Request().Context())
	if err != nil {
		return notFoundOr(err)
	}
	return c.JSON(http.StatusOK, toKBFile(nf))
}

func mountKB(g *echo.Group, d Deps, az *authz.Service, gw *gateway.Service) {
	store := d.KBStore
	if store == nil {
		store = kb.NewMemStore()
	}
	maxB := d.KBFileMaxBytes
	if maxB <= 0 {
		maxB = 50 << 20
	}
	enq := d.KBEnqueue
	if enq == nil {
		var objs kb.Objects
		if d.Files != nil {
			objs = d.Files
		}
		enq = kb.InlineEnqueuer{X: &kb.Indexer{DB: d.DB, Objects: objs, Store: store, GW: gw, Parser: docparse.New(), Log: d.Log, Settle: d.Budget.Settle}}
	}
	lg := d.Log
	if lg == nil {
		lg = slog.Default()
	}
	h := &kbHandlers{db: d.DB, az: az, gw: gw, fs: d.Files, store: store, enq: enq, maxB: maxB, log: lg, auth: d.Auth,
		now: time.Now, detect: docparse.Detect}
	g.GET("/kbs", h.list)
	g.POST("/kbs", h.create, authz.Require(rbac.KBCreate))
	g.GET("/kbs/:id", h.get)
	g.PATCH("/kbs/:id", h.update)
	g.DELETE("/kbs/:id", h.remove)
	g.GET("/kbs/:id/acl", h.listACL)
	g.POST("/kbs/:id/acl", h.grantACL)
	g.DELETE("/kbs/:id/acl/:type/:sid", h.revokeACL)
	g.GET("/kbs/:id/files", h.listFiles)
	g.POST("/kbs/:id/files", h.upload)
	g.DELETE("/kbs/:id/files/:fid", h.removeFile)
	g.POST("/kbs/:id/files/:fid/retry", h.retryFile)
	g.GET("/kbs/:id/files/:fid/acl", h.listFileACL)
	g.POST("/kbs/:id/files/:fid/acl", h.grantFileACL)
	g.DELETE("/kbs/:id/files/:fid/acl/:type/:sid", h.revokeFileACL)
	g.PUT("/kbs/:id/files/:fid/acl-mode", h.setFileACLMode)
}
