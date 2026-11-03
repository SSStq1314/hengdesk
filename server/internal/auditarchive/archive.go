// Package auditarchive 审计保留期与归档（FR-40 / D-34 第 4 点）。
//
// 归档顺序（任何一步失败都不删除任何记录，fail-closed）：
//  1. 取出保留期之前的记录（ID 连续的前缀），逐条校验哈希链；
//  2. 生成归档包（records.jsonl + manifest + SHA256SUMS [+ 签名]）并写入 S3 兼容存储，再读回比对 sha256；
//  3. 在审计链上写"归档断点"记录（记录被归档区间的链尾哈希、归档包 sha256 与对象键）；
//  4. 在一个事务里删除已归档记录：数据库触发器只放行"有对应断点、且 ID 在断点区间内"的 DELETE；
//     删除条数与归档条数不符则回滚。
//
// 系统不会自动删除审计记录：归档只由审计员手动执行 `hengdesk audit archive`。
package auditarchive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
)

// ObjectStore 归档包写入的对象存储（objstore.Store 满足该接口）。
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// ErrNothingToArchive 没有超过保留期的记录。
var ErrNothingToArchive = errors.New("没有超过保留期的审计记录")

// Archiver 执行归档。
type Archiver struct {
	C             *ent.Client
	DB            *sql.DB // 删除走原生 SQL（ent hook 拒绝一切非新增操作）
	W             audit.Writer
	S             ObjectStore
	RetentionDays int
	SignKey       ed25519.PrivateKey // 可选
	Prefix        string             // 对象键前缀，默认 audit-archives/
	Now           func() time.Time
	MaxRecords    int // 单次最多归档条数，默认 500000

	// 测试注入：在指定步骤之后模拟失败（"uploaded" / "checkpointed"）。
	failAfter string
}

// Result 归档结果。
type Result struct {
	ArchiveID    string         `json:"archive_id"`
	ObjectKey    string         `json:"object_key"`
	SHA256       string         `json:"sha256"`
	Size         int            `json:"size"`
	CheckpointID int            `json:"checkpoint_id"`
	Deleted      int            `json:"deleted"`
	Manifest     audit.Manifest `json:"manifest"`
}

func (a *Archiver) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// Cutoff 早于该时间的记录超过保留期。
func (a *Archiver) Cutoff() time.Time {
	return a.now().AddDate(0, 0, -a.RetentionDays)
}

// Run 执行一次归档。actor 为执行人（写入断点记录）。
func (a *Archiver) Run(ctx context.Context, actor audit.Entry) (*Result, error) {
	if a.RetentionDays < 1 {
		return nil, errors.New("保留期未配置")
	}
	max := a.MaxRecords
	if max <= 0 {
		max = 500000
	}
	cutoff := a.Cutoff()
	// 1. 区间：ts < cutoff 的最大 ID 及其之前的全部记录（ID 连续前缀，剩余链从下一条开始）。
	lastRow, err := a.C.AuditLog.Query().Where(auditlog.TsLT(cutoff)).Order(ent.Desc(auditlog.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNothingToArchive
	}
	if err != nil {
		return nil, err
	}
	rows, err := a.C.AuditLog.Query().Where(auditlog.IDLTE(lastRow.ID)).Order(ent.Asc(auditlog.FieldID)).Limit(max).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNothingToArchive
	}
	recs := make([]audit.ArchiveRecord, len(rows))
	for i, r := range rows {
		recs[i] = audit.RecordFromEnt(r)
	}
	lastID := recs[len(recs)-1].ID
	// 锚点：区间首条有 prev_hash 时，必须由在线链上的上一次归档断点担保。
	anchor := ""
	for _, r := range recs {
		if r.Hash != "" {
			if r.PrevHash != "" {
				id, err := audit.Anchor(ctx, a.C, r.PrevHash, r.ID)
				if err != nil {
					return nil, err
				}
				if id == 0 {
					return nil, fmt.Errorf("记录 %d 的 prev_hash 没有对应的归档断点，链已断，拒绝归档", r.ID)
				}
				anchor = r.PrevHash
			}
			break
		}
	}
	first, last, chained, err := audit.ChainSegment(recs, anchor)
	if err != nil {
		return nil, fmt.Errorf("待归档记录校验失败，拒绝归档: %w", err)
	}
	// 区间之后的第一条链上记录必须接在 last 之后，否则删除后无法衔接。
	if last != "" {
		next, err := a.C.AuditLog.Query().Where(auditlog.IDGT(lastID), auditlog.HashNEQ("")).Order(ent.Asc(auditlog.FieldID)).First(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, err
		}
		if next != nil && next.PrevHash != last {
			return nil, fmt.Errorf("记录 %d 未接在归档区间之后，链已断，拒绝归档", next.ID)
		}
	}
	// 2. 归档包
	var rb [6]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return nil, err
	}
	now := a.now()
	id := now.Format("20060102T150405Z") + "-" + hex.EncodeToString(rb[:])
	m := audit.Manifest{ArchiveID: id, CreatedAt: now, Cutoff: cutoff, RetentionDays: a.RetentionDays,
		FirstID: recs[0].ID, LastID: lastID, Count: len(recs), Chained: chained,
		FirstPrevHash: anchor, FirstHash: first, LastHash: last}
	pkg, m, err := audit.BuildPackage(m, recs, a.SignKey)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(pkg)
	digest := hex.EncodeToString(sum[:])
	prefix := a.Prefix
	if prefix == "" {
		prefix = "audit-archives/"
	}
	key := prefix + id + ".tar.gz"
	if err := a.S.Put(ctx, key, bytes.NewReader(pkg), int64(len(pkg)), "application/gzip"); err != nil {
		return nil, fmt.Errorf("写入对象存储失败（未删除任何记录）: %w", err)
	}
	if err := a.readBack(ctx, key, digest); err != nil {
		return nil, err
	}
	res := &Result{ArchiveID: id, ObjectKey: key, SHA256: digest, Size: len(pkg), Manifest: m}
	if a.failAfter == "uploaded" {
		return res, errors.New("injected failure after upload")
	}
	// 3. 断点（fail-closed：写不进审计链就不删）
	cp := actor
	cp.Action, cp.Phase, cp.Outcome = audit.CheckpointAction, "result", "success"
	cp.ResourceType, cp.ResourceID = "audit_archive", id
	cp.Detail = map[string]any{"archive_id": id, "object_key": key, "sha256": digest, "size": len(pkg),
		"first_id": m.FirstID, "last_id": m.LastID, "count": m.Count, "chained": m.Chained,
		"first_prev_hash": m.FirstPrevHash, "first_hash": m.FirstHash, "last_hash": m.LastHash,
		"cutoff": cutoff.Format(time.RFC3339), "retention_days": a.RetentionDays}
	if err := a.W.Write(ctx, cp); err != nil {
		return res, fmt.Errorf("写归档断点失败（未删除任何记录）: %w", err)
	}
	cpRow, err := a.C.AuditLog.Query().Where(auditlog.Action(audit.CheckpointAction), auditlog.ResourceID(id)).Only(ctx)
	if err != nil {
		return res, fmt.Errorf("读取归档断点失败（未删除任何记录）: %w", err)
	}
	res.CheckpointID = cpRow.ID
	if a.failAfter == "checkpointed" {
		return res, errors.New("injected failure after checkpoint")
	}
	// 4. 删除（单事务；条数不符回滚）
	n, err := a.purge(ctx, cpRow.ID, m.FirstID, lastID, m.Count)
	if err != nil {
		return res, fmt.Errorf("删除已归档记录失败，已回滚（归档包与断点保留，可重新执行）: %w", err)
	}
	res.Deleted = n
	return res, nil
}

func (a *Archiver) readBack(ctx context.Context, key, digest string) error {
	rc, err := a.S.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("读回归档包失败（未删除任何记录）: %w", err)
	}
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return fmt.Errorf("读回归档包失败（未删除任何记录）: %w", err)
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return errors.New("对象存储中的归档包与本地不一致（未删除任何记录）")
	}
	return nil
}

func (a *Archiver) purge(ctx context.Context, cpID, firstID, lastID, want int) (int, error) {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('hengdesk.audit_archive_checkpoint', $1, true)`, strconv.Itoa(cpID)); err != nil {
		return 0, err
	}
	r, err := tx.ExecContext(ctx, `DELETE FROM audit_logs WHERE id >= $1 AND id <= $2`, firstID, lastID)
	if err != nil {
		return 0, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, err
	}
	if int(n) != want {
		return 0, fmt.Errorf("删除条数 %d 与归档条数 %d 不一致", n, want)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}
