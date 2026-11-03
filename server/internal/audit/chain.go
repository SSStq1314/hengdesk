package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
)

// 哈希链：每条记录 hash = sha256(prev_hash ‖ 规范化 JSON)。prev_hash 上有唯一索引，
// 多实例并发写入时链不会分叉，冲突方重读链尾后重试。

var chainMu sync.Mutex

const chainRetries = 8

// canonical 计算哈希的规范化内容。时间截断到微秒并转 UTC，与 PostgreSQL timestamptz 精度一致。
func canonical(prev string, ts time.Time, orgID, actorID int, email, action, rtype, rid, phase, outcome, ip, ua, reqID, traceID string, detail map[string]any) string {
	d, _ := json.Marshal(detail)
	b, _ := json.Marshal([]any{prev, ts.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano),
		orgID, actorID, email, action, rtype, rid, phase, outcome, ip, ua, reqID, traceID, string(d)})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func rowHash(r *ent.AuditLog) string {
	return canonical(r.PrevHash, r.Ts, r.OrgID, r.ActorID, r.ActorEmail, r.Action, r.ResourceType, r.ResourceID,
		string(r.Phase), r.Outcome, r.IP, r.UserAgent, r.RequestID, r.TraceID, r.Detail)
}

func (w EntWriter) tip(ctx context.Context) (string, error) {
	r, err := w.C.AuditLog.Query().Where(auditlog.HashNEQ("")).Order(ent.Desc(auditlog.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return r.Hash, nil
}

// VerifyResult 校验结果。
type VerifyResult struct {
	OK       bool `json:"ok"`
	Checked  int  `json:"checked"`
	Legacy   int  `json:"legacy"`              // 未入链的旧记录（M1 之前）
	BrokenAt int  `json:"broken_at,omitempty"` // 第一条校验失败的记录 ID
}

// Verify 按 ID 顺序校验整条链：内容哈希一致且 prev_hash 指向上一条。
func Verify(ctx context.Context, c *ent.Client) (VerifyResult, error) {
	var res VerifyResult
	legacy, err := c.AuditLog.Query().Where(auditlog.Hash("")).Count(ctx)
	if err != nil {
		return res, err
	}
	res.Legacy = legacy
	prev, after := "", 0
	for {
		rows, err := c.AuditLog.Query().Where(auditlog.HashNEQ(""), auditlog.IDGT(after)).
			Order(ent.Asc(auditlog.FieldID)).Limit(1000).All(ctx)
		if err != nil {
			return res, err
		}
		if len(rows) == 0 {
			res.OK = true
			return res, nil
		}
		for _, r := range rows {
			if r.PrevHash != prev || rowHash(r) != r.Hash {
				res.BrokenAt = r.ID
				return res, nil
			}
			prev, after = r.Hash, r.ID
			res.Checked++
		}
	}
}
