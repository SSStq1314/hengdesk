package auditarchive

import (
	"context"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
)

// 保留期提醒：最早的在线记录距超过保留期不足 WarnDays 天时，给审计员发站内提醒（复用 D-34 高风险提醒）。
const (
	WarnDays          = 30
	AlertRetentionDue = "audit_retention_due"
	SeverityMedium    = "medium"
	remindEvery       = 7 * 24 * time.Hour
	// TaskRemind worker 定时任务（asynq），只做到期检查与提醒，不归档、不删除。
	TaskRemind = "audit:retention_remind"
)

// Status 保留期状态。
type Status struct {
	RetentionDays int        `json:"retention_days"`
	OnlineCount   int        `json:"online_count"`
	OldestID      int        `json:"oldest_id,omitempty"`
	OldestTs      *time.Time `json:"oldest_ts,omitempty"`
	DueAt         *time.Time `json:"due_at,omitempty"` // 最早一条超过保留期的时间
	DaysLeft      int        `json:"days_left"`
	DueSoon       bool       `json:"due_soon"` // 30 天内到期或已到期
	Overdue       bool       `json:"overdue"`  // 已有记录超过保留期，待归档
	LastArchiveID string     `json:"last_archive_id,omitempty"`
}

// Check 计算保留期状态。
func Check(ctx context.Context, c *ent.Client, retentionDays int, now time.Time) (Status, error) {
	st := Status{RetentionDays: retentionDays}
	n, err := c.AuditLog.Query().Count(ctx)
	if err != nil {
		return st, err
	}
	st.OnlineCount = n
	if cp, err := c.AuditLog.Query().Where(auditlog.Action(audit.CheckpointAction)).Order(ent.Desc(auditlog.FieldID)).First(ctx); err == nil {
		st.LastArchiveID = cp.ResourceID
	} else if !ent.IsNotFound(err) {
		return st, err
	}
	oldest, err := c.AuditLog.Query().Order(ent.Asc(auditlog.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	ts := oldest.Ts.UTC()
	due := ts.AddDate(0, 0, retentionDays)
	st.OldestID, st.OldestTs, st.DueAt = oldest.ID, &ts, &due
	left := due.Sub(now)
	st.DaysLeft = int(left.Hours() / 24)
	if left < 0 {
		st.DaysLeft = -int((-left).Hours()/24) - 1
	}
	st.Overdue = !now.Before(due)
	st.DueSoon = left < WarnDays*24*time.Hour
	return st, nil
}

// Remind 到期前 30 天起提醒审计员；同一批记录每 7 天最多提醒一次。返回是否发出了提醒。
func Remind(ctx context.Context, c *ent.Client, w audit.Writer, retentionDays int, now time.Time) (bool, Status, error) {
	st, err := Check(ctx, c, retentionDays, now)
	if err != nil || !st.DueSoon {
		return false, st, err
	}
	recent, err := c.AuditLog.Query().Where(auditlog.Action(auth.AlertAction), auditlog.TsGT(now.Add(-remindEvery))).All(ctx)
	if err != nil {
		return false, st, err
	}
	for _, r := range recent {
		if k, _ := r.Detail["kind"].(string); k == AlertRetentionDue {
			if id, _ := r.Detail["oldest_id"].(float64); int(id) == st.OldestID {
				return false, st, nil
			}
		}
	}
	err = w.Write(ctx, audit.Entry{Action: auth.AlertAction, Phase: "result", Outcome: "success",
		ResourceType: "audit_logs", ResourceID: "retention", ActorEmail: "system",
		Detail: map[string]any{"kind": AlertRetentionDue, "severity": SeverityMedium, "oldest_id": st.OldestID,
			"due_at": st.DueAt.Format(time.RFC3339), "days_left": st.DaysLeft, "retention_days": retentionDays}})
	return err == nil, st, err
}
