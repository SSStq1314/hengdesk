// Package chatretention 对话消息保留期清理（FR-40，D-40 第 2 条）。
//
// 规则：
//   - 默认不开启清理；开启后默认保留 365 天，最低 180 天。
//   - 以会话为单位：最后活动时间（会话创建时间和最后一条消息时间中较晚者）早于保留期的会话，
//     连同消息、会话文件（含对象存储原件）一起物理删除。
//   - 软删除的会话按同一保留期清理：按最后活动时间计算，不因软删除提前，也不因软删除推后。
//   - 处于法律保留的数据不清理：全组织保留时整个组织跳过，按用户保留时跳过该用户的会话。
//   - 清理记审计：操作人（定时任务为 system）、时间范围、条数。attempt 写失败不清理；
//     result 审计与删除同事务，写不进去整体回滚（fail-closed）。
package chatretention

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/conversation"
	"github.com/SSStq1314/hengdesk/server/internal/ent/conversationfile"
	"github.com/SSStq1314/hengdesk/server/internal/ent/legalhold"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
	"github.com/SSStq1314/hengdesk/server/internal/ent/systemsetting"
)

const (
	SettingKey  = "chat.retention"
	DefaultDays = 365
	MinDays     = 180
	// TaskPurge worker 定时任务（asynq）。未开启时直接跳过。
	TaskPurge = "chat:retention_purge"
	// Action 清理审计动作。
	Action    = "chat.retention.purge"
	batchSize = 500
)

// ErrDisabled 未开启清理。
var ErrDisabled = errors.New("chat retention disabled")

// ErrAudit attempt 审计写失败（fail-closed，未删除任何数据）。
var ErrAudit = errors.New("chat retention audit unavailable")

// Settings 保留期设置。
type Settings struct {
	Enabled bool `json:"enabled"`
	Days    int  `json:"days"`
}

// Validate 校验天数（最低 180 天）。
func (s Settings) Validate() error {
	if s.Days < MinDays {
		return fmt.Errorf("保留期最低 %d 天", MinDays)
	}
	if s.Days > 36500 {
		return errors.New("保留期过长")
	}
	return nil
}

// Load 读取设置；没有记录时为默认值（不开启，365 天）。
func Load(ctx context.Context, c *ent.Client) (Settings, error) {
	st := Settings{Days: DefaultDays}
	s, err := c.SystemSetting.Query().Where(systemsetting.Key(SettingKey)).Only(ctx)
	if ent.IsNotFound(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if v, ok := s.Value["enabled"].(bool); ok {
		st.Enabled = v
	}
	if v, ok := s.Value["days"].(float64); ok && int(v) >= MinDays {
		st.Days = int(v)
	}
	return st, nil
}

// Save 写设置（调用方负责审计）。
func Save(ctx context.Context, c *ent.Client, st Settings, by int) error {
	if err := st.Validate(); err != nil {
		return err
	}
	v := map[string]any{"enabled": st.Enabled, "days": st.Days}
	n, err := c.SystemSetting.Update().Where(systemsetting.Key(SettingKey)).SetValue(v).SetUpdatedBy(by).Save(ctx)
	if err != nil || n > 0 {
		return err
	}
	return c.SystemSetting.Create().SetKey(SettingKey).SetValue(v).SetUpdatedBy(by).Exec(ctx)
}

// Operator 清理的操作人。定时任务为 system。
type Operator struct {
	OrgID   int
	ActorID int
	Email   string
	Trigger string // scheduled / manual
	IP      string
	UA      string
	Request string
	Trace   string
}

// Result 一次清理的结果。
type Result struct {
	Cutoff        time.Time  `json:"cutoff"`
	OldestAt      *time.Time `json:"oldest_at,omitempty"`
	NewestAt      *time.Time `json:"newest_at,omitempty"`
	Conversations int        `json:"conversations"`
	Messages      int        `json:"messages"`
	Files         int        `json:"files"`
	HeldOrgs      int        `json:"held_orgs"`
	HeldUsers     int        `json:"held_users"`
	ObjectErrors  int        `json:"object_errors"`
}

// ObjectDeleter 删除对象存储原件（可为 nil：未配置对象存储）。
type ObjectDeleter interface {
	Delete(ctx context.Context, key string) error
}

// Holds 当前生效的法律保留。
func Holds(ctx context.Context, c *ent.Client) (orgs map[int]bool, users map[int]bool, err error) {
	hs, err := c.LegalHold.Query().Where(legalhold.ReleasedAtIsNil()).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	orgs, users = map[int]bool{}, map[int]bool{}
	for _, h := range hs {
		if h.UserID == nil {
			orgs[h.OrgID] = true
		} else {
			users[*h.UserID] = true
		}
	}
	return orgs, users, nil
}

func writerFor(w audit.Writer, tx *ent.Tx) audit.Writer {
	if _, ok := w.(audit.EntWriter); ok {
		return audit.EntWriter{C: tx.Client()}
	}
	return w
}

func keys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Purge 执行一次清理。未开启返回 ErrDisabled（什么也不做）。按批删除，每批一对审计（attempt + 同事务 result）。
func Purge(ctx context.Context, c *ent.Client, w audit.Writer, objs ObjectDeleter, op Operator, now time.Time) (Result, error) {
	st, err := Load(ctx, c)
	if err != nil {
		return Result{}, err
	}
	if !st.Enabled {
		return Result{}, ErrDisabled
	}
	res := Result{Cutoff: now.AddDate(0, 0, -st.Days).UTC()}
	heldOrgs, heldUsers, err := Holds(ctx, c)
	if err != nil {
		return res, err
	}
	res.HeldOrgs, res.HeldUsers = len(heldOrgs), len(heldUsers)
	base := audit.Entry{OrgID: op.OrgID, ActorID: op.ActorID, ActorEmail: op.Email, Action: Action,
		ResourceType: "conversation", ResourceID: "retention", IP: op.IP, UserAgent: op.UA, RequestID: op.Request, TraceID: op.Trace}
	for {
		q := c.Conversation.Query().Where(conversation.CreatedAtLT(res.Cutoff),
			conversation.Not(conversation.HasMessagesWith(message.CreatedAtGTE(res.Cutoff))))
		if len(heldOrgs) > 0 {
			q = q.Where(conversation.OrgIDNotIn(keys(heldOrgs)...))
		}
		if len(heldUsers) > 0 {
			q = q.Where(conversation.UserIDNotIn(keys(heldUsers)...))
		}
		cvs, err := q.Order(ent.Asc(conversation.FieldID)).Limit(batchSize).All(ctx)
		if err != nil {
			return res, err
		}
		if len(cvs) == 0 {
			break
		}
		ids := make([]int, len(cvs))
		for i, cv := range cvs {
			ids[i] = cv.ID
		}
		// 时间范围：本批最早的会话创建时间 ~ 截止时间（被清理的数据都早于截止时间）。
		from, to := cvs[0].CreatedAt.UTC(), res.Cutoff
		for _, cv := range cvs {
			if cv.CreatedAt.Before(from) {
				from = cv.CreatedAt.UTC()
			}
		}
		detail := map[string]any{"trigger": op.Trigger, "retention_days": st.Days, "cutoff": res.Cutoff.Format(time.RFC3339),
			"from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339), "conversations": len(ids),
			"held_orgs": res.HeldOrgs, "held_users": res.HeldUsers}
		at := base
		at.Phase, at.Detail = "attempt", detail
		if err := w.Write(ctx, at); err != nil {
			return res, fmt.Errorf("%w: %w", ErrAudit, err) // fail-closed：不删除
		}
		var objKeys []string
		var nMsg, nFile int
		tx, err := c.Tx(ctx)
		if err != nil {
			return res, err
		}
		err = func() error {
			fs, err := tx.ConversationFile.Query().Where(conversationfile.ConversationIDIn(ids...)).All(ctx)
			if err != nil {
				return err
			}
			for _, f := range fs {
				objKeys = append(objKeys, f.ObjectKey)
			}
			if nFile, err = tx.ConversationFile.Delete().Where(conversationfile.ConversationIDIn(ids...)).Exec(ctx); err != nil {
				return err
			}
			if nMsg, err = tx.Message.Delete().Where(message.ConversationIDIn(ids...)).Exec(ctx); err != nil {
				return err
			}
			if _, err := tx.Conversation.Delete().Where(conversation.IDIn(ids...)).Exec(ctx); err != nil {
				return err
			}
			rd := map[string]any{"messages": nMsg, "files": nFile}
			for k, v := range detail {
				rd[k] = v
			}
			r := base
			r.Phase, r.Outcome, r.Detail = "result", "success", rd
			return writerFor(w, tx).Write(ctx, r)
		}()
		if err != nil {
			_ = tx.Rollback()
			return res, err
		}
		if err := tx.Commit(); err != nil {
			return res, err
		}
		if res.OldestAt == nil || from.Before(*res.OldestAt) {
			res.OldestAt = &from
		}
		res.NewestAt = &to
		res.Conversations += len(ids)
		res.Messages += nMsg
		res.Files += nFile
		if objs != nil {
			for _, k := range objKeys {
				if err := objs.Delete(context.WithoutCancel(ctx), k); err != nil {
					res.ObjectErrors++
				}
			}
		}
		if len(cvs) < batchSize {
			break
		}
	}
	return res, nil
}
