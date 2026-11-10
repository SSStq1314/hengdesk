package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/usagehourly"
	"github.com/SSStq1314/hengdesk/server/internal/ent/usagerecord"
)

// TaskAggregate worker 定时任务：按小时重建 usage_hourly。
const TaskAggregate = "usage:hourly_aggregate"

// HourBucket 将 t 按 loc 切到整点，再以 UTC 返回（与库中 timestamptz 一致）。
func HourBucket(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), 0, 0, 0, loc).UTC()
}

type hourKey struct {
	Org, User, Dept, Model, Agent int
	Bucket                        time.Time
}

// RebuildHours 删除 [from,to) 内的小时桶并按 usage_records 重建（幂等）。from/to 会按 loc 对齐到整点。
func RebuildHours(ctx context.Context, db *ent.Client, loc *time.Location, from, to time.Time) (int, error) {
	if loc == nil {
		var err error
		loc, err = time.LoadLocation("Asia/Shanghai")
		if err != nil {
			return 0, err
		}
	}
	if !to.After(from) {
		return 0, nil
	}
	start := HourBucket(from, loc)
	end := HourBucket(to.Add(-time.Nanosecond), loc).Add(time.Hour)
	if _, err := db.UsageHourly.Delete().Where(usagehourly.BucketAtGTE(start), usagehourly.BucketAtLT(end)).Exec(ctx); err != nil {
		return 0, err
	}
	// 读出后在内存按小时桶聚合。查询边界用 loc 墙钟时间（SQLite 按字符串比较时区后缀，UTC 与 +0800 对不上）。
	rs, err := db.UsageRecord.Query().Where(
		usagerecord.CreatedAtGTE(start.In(loc)),
		usagerecord.CreatedAtLT(end.In(loc)),
	).All(ctx)
	if err != nil {
		return 0, err
	}
	type cell struct {
		Totals
		Org, User, Dept, Model, Agent int
		Bucket                        time.Time
	}
	idx := map[hourKey]*cell{}
	order := []hourKey{}
	for _, r := range rs {
		b := HourBucket(r.CreatedAt, loc)
		if b.Before(start) || !b.Before(end) {
			continue
		}
		dept, agent := 0, 0
		if r.DepartmentID != nil {
			dept = *r.DepartmentID
		}
		if r.AgentID != nil {
			agent = *r.AgentID
		}
		k := hourKey{Org: r.OrgID, User: r.UserID, Dept: dept, Model: r.ModelID, Agent: agent, Bucket: b}
		c, ok := idx[k]
		if !ok {
			c = &cell{Org: r.OrgID, User: r.UserID, Dept: dept, Model: r.ModelID, Agent: agent, Bucket: b}
			idx[k] = c
			order = append(order, k)
		}
		c.Calls++
		if r.Status == usagerecord.StatusError {
			c.ErrorCalls++
		}
		c.PromptTokens += int64(r.PromptTokens)
		c.CachedTokens += int64(r.CachedTokens)
		c.CompletionTokens += int64(r.CompletionTokens)
		c.ReasoningTokens += int64(r.ReasoningTokens)
		c.TotalTokens += int64(r.TotalTokens)
		c.CostMicros += r.CostMicros
		if r.UsageUnknown {
			c.UnknownCalls++
		}
		if r.Estimated {
			c.EstimatedCalls++
			c.EstimatedTokens += int64(r.TotalTokens)
			c.EstimatedCostMicros += r.CostMicros
		}
	}
	builders := make([]*ent.UsageHourlyCreate, 0, len(order))
	now := time.Now().UTC()
	for _, k := range order {
		c := idx[k]
		builders = append(builders, db.UsageHourly.Create().
			SetOrgID(c.Org).SetBucketAt(c.Bucket).SetUserID(c.User).SetDepartmentID(c.Dept).
			SetModelID(c.Model).SetAgentID(c.Agent).
			SetCalls(c.Calls).SetErrorCalls(c.ErrorCalls).
			SetPromptTokens(c.PromptTokens).SetCachedTokens(c.CachedTokens).
			SetCompletionTokens(c.CompletionTokens).SetReasoningTokens(c.ReasoningTokens).
			SetTotalTokens(c.TotalTokens).SetCostMicros(c.CostMicros).
			SetEstimatedCalls(c.EstimatedCalls).SetEstimatedTokens(c.EstimatedTokens).
			SetEstimatedCostMicros(c.EstimatedCostMicros).SetUnknownCalls(c.UnknownCalls).
			SetUpdatedAt(now))
	}
	const batch = 200
	n := 0
	for i := 0; i < len(builders); i += batch {
		j := i + batch
		if j > len(builders) {
			j = len(builders)
		}
		if err := db.UsageHourly.CreateBulk(builders[i:j]...).Exec(ctx); err != nil {
			return n, fmt.Errorf("usage hourly insert: %w", err)
		}
		n += j - i
	}
	return n, nil
}

// RebuildRecent 重建「上一完整小时」与「当前小时」两桶（定时任务默认）。
func RebuildRecent(ctx context.Context, db *ent.Client, loc *time.Location, now time.Time) (int, error) {
	cur := HourBucket(now, loc)
	return RebuildHours(ctx, db, loc, cur.Add(-time.Hour), cur.Add(time.Hour))
}
