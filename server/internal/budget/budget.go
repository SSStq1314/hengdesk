// Package budget 预算与配额（FR-13 / AC-10 / D-23）。
//
// 数据分工：usage_records 是权威数据；Valkey 只存当期实时计数（带 TTL 的缓存），丢了或漂了都能从 usage_records 重建。
//
//   - 预检查（Precheck）：调用上游前读取所有适用预算的当期已用量，任一「拦截」型预算已用 ≥ 上限即拒绝（取最严）。
//     Valkey 不可用且存在适用预算时 fail-closed（拒绝调用）。
//   - 事后结算（Settle）：usage_record 落库后按实际用量（流式在结束后）累加计数；计数不存在时从库里重建。
//     「预检查 + 事后结算」意味着单次请求可能让用量略超上限，超出部分照实记录、界面可见。
//   - 告警：越过阈值（默认 80%）与达到 100% 时，各发一次站内提醒并写审计（同一预算同一周期同一级别只发一次）。
//   - 对账（Reconcile）：比较 Valkey 计数与 usage_records 汇总，并以库为准重置计数。
package budget

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"
	_ "time/tzdata"

	"entgo.io/ent/dialect/sql"
	"github.com/redis/go-redis/v9"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/budget"
	"github.com/SSStq1314/hengdesk/server/internal/ent/deptclosure"
	"github.com/SSStq1314/hengdesk/server/internal/ent/predicate"
	"github.com/SSStq1314/hengdesk/server/internal/ent/usagerecord"
)

const (
	// CodeExceeded 预算已用尽，调用被拦截（HTTP 429）。
	CodeExceeded = "budget_exceeded"
	// CodeUnavailable 预算计数服务（Valkey）不可用，按 fail-closed 拒绝调用（HTTP 503）。
	CodeUnavailable = "budget_unavailable"

	// counterTTL 计数缓存有效期：到期后下一次读取从 usage_records 重建，漂移最多持续这么久。
	counterTTL = 15 * time.Minute
)

// ErrUnavailable Valkey 不可用。
var ErrUnavailable = errors.New("budget: counter store unavailable")

// Service 预算服务。
type Service struct {
	DB  *ent.Client
	R   redis.UniversalClient
	Log *slog.Logger
	// Loc 周期边界所在时区（默认服务器本地时区）。
	Loc *time.Location
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return defaultLoc
}

// defaultLoc 未配置时的系统时区（D-37 第 6 条：默认 Asia/Shanghai，不依赖容器 TZ）。
var defaultLoc = func() *time.Location {
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return l
}()

// Logger 服务日志。
func (s *Service) Logger() *slog.Logger { return s.log() }

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Subject 一次调用的归属。DeptID 为调用人主部门（与 usage_record 的快照一致）。
type Subject struct {
	OrgID   int
	UserID  int
	DeptID  *int
	ModelID int
	AgentID *int // 智能体会话（M1 第六块 PR3）；非智能体调用为 nil
}

// Window 预算周期。
type Window struct {
	Start, End time.Time
	Key        string
}

// PeriodWindow now 所在的周期 [Start, End)。
func PeriodWindow(period budget.Period, now time.Time, loc *time.Location) Window {
	t := now.In(loc)
	var st, en time.Time
	if period == budget.PeriodMonth {
		st = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
		en = st.AddDate(0, 1, 0)
	} else {
		st = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		en = st.AddDate(0, 0, 1)
	}
	return Window{Start: st, End: en, Key: st.Format("20060102")}
}

// Window b 当前周期。
func (s *Service) Window(b *ent.Budget) Window { return PeriodWindow(b.Period, s.now(), s.loc()) }

func counterKey(b *ent.Budget, w Window) string {
	// 带上 updated_at：预算被修改（维度 / 指标 / 周期）后旧计数自动失效。
	return fmt.Sprintf("budget:used:%d:%d:%s", b.ID, b.UpdatedAt.UnixMilli(), w.Key)
}

// Applicable 对 sub 生效的已启用预算：组织、主部门及其上级部门（部门预算含下级）、本人、所用模型。
func (s *Service) Applicable(ctx context.Context, sub Subject) ([]*ent.Budget, error) {
	or := []predicate.Budget{budget.ScopeEQ(budget.ScopeOrg),
		budget.And(budget.ScopeEQ(budget.ScopeUser), budget.ScopeID(sub.UserID)),
		budget.And(budget.ScopeEQ(budget.ScopeModel), budget.ScopeID(sub.ModelID))}
	if sub.DeptID != nil {
		anc, err := s.DB.DeptClosure.Query().Where(deptclosure.OrgID(sub.OrgID), deptclosure.DescendantID(*sub.DeptID)).
			Unique(true).Select(deptclosure.FieldAncestorID).Ints(ctx)
		if err != nil {
			return nil, err
		}
		if len(anc) == 0 {
			anc = []int{*sub.DeptID}
		}
		or = append(or, budget.And(budget.ScopeEQ(budget.ScopeDept), budget.ScopeIDIn(anc...)))
	}
	if sub.AgentID != nil {
		or = append(or, budget.And(budget.ScopeEQ(budget.ScopeAgent), budget.ScopeID(*sub.AgentID)))
	}
	return s.DB.Budget.Query().Where(budget.OrgID(sub.OrgID), budget.Enabled(true), budget.Or(or...)).
		Order(ent.Asc(budget.FieldID)).All(ctx)
}

// DBUsed 从 usage_records 汇总 b 在 w 内的已用量（权威值）。
// 计入所有用量记录（对话、模型测试、带 usage 的失败调用）；上游超时 / 5xx 且无 usage 的记录为 0，不占预算。
func (s *Service) DBUsed(ctx context.Context, b *ent.Budget, w Window) (int64, error) {
	// SQLite 把 datetime 存成进程本地墙钟（无时区）。周期按 D-37 在 Asia/Shanghai 切开。
	// 比较前把边界转到 time.Local，才能同时覆盖：
	// - TZ=UTC（#55）：不能用带 +08:00 的墙钟去比 UTC 存储的 created_at；
	// - TZ=Asia/Shanghai 且当地 ≥ 16:00：不能用 End.UTC() 的 16:00 墙钟去比 16:12 的本地存储。
	// PostgreSQL timestamptz 按瞬时比较，In(time.Local) 与 UTC() 是同一瞬间。
	ps := []predicate.UsageRecord{usagerecord.OrgID(b.OrgID), usagerecord.CreatedAtGTE(w.Start.In(time.Local)), usagerecord.CreatedAtLT(w.End.In(time.Local))}
	switch b.Scope {
	case budget.ScopeUser:
		ps = append(ps, usagerecord.UserID(b.ScopeID))
	case budget.ScopeModel:
		ps = append(ps, usagerecord.ModelID(b.ScopeID))
	case budget.ScopeDept:
		ds, err := s.DB.DeptClosure.Query().Where(deptclosure.OrgID(b.OrgID), deptclosure.AncestorID(b.ScopeID)).
			Unique(true).Select(deptclosure.FieldDescendantID).Ints(ctx)
		if err != nil {
			return 0, err
		}
		ds = append(ds, b.ScopeID)
		ps = append(ps, usagerecord.DepartmentIDIn(ds...))
	case budget.ScopeAgent:
		ps = append(ps, usagerecord.AgentID(b.ScopeID))
	}
	col := usagerecord.FieldTotalTokens
	if b.Metric == budget.MetricCost {
		col = usagerecord.FieldCostMicros
	}
	var v []struct {
		Sum sql.NullInt64 `json:"sum"`
	}
	if err := s.DB.UsageRecord.Query().Where(ps...).Aggregate(ent.Sum(col)).Scan(ctx, &v); err != nil {
		return 0, err
	}
	if len(v) == 0 {
		return 0, nil
	}
	return v[0].Sum.Int64, nil
}

// Used 当期已用量：优先读 Valkey，缺失时从库重建并写回。Valkey 出错返回 ErrUnavailable。
func (s *Service) Used(ctx context.Context, b *ent.Budget) (int64, error) {
	w := s.Window(b)
	k := counterKey(b, w)
	v, err := s.R.Get(ctx, k).Int64()
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, redis.Nil) {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return s.rebuild(ctx, b, w, k)
}

func (s *Service) rebuild(ctx context.Context, b *ent.Budget, w Window, k string) (int64, error) {
	n, err := s.DBUsed(ctx, b, w)
	if err != nil {
		return 0, err
	}
	ok, err := s.R.SetNX(ctx, k, n, counterTTL).Result()
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !ok { // 并发重建：以先写入的为准
		if v, err := s.R.Get(ctx, k).Int64(); err == nil {
			return v, nil
		}
	}
	return n, nil
}

// Exceeded 预检查拦截信息（可读，返回给调用方）。
type Exceeded struct {
	BudgetID int    `json:"budget_id"`
	Scope    string `json:"scope"`
	Period   string `json:"period"`
	Metric   string `json:"metric"`
	Limit    int64  `json:"limit"`
	Used     int64  `json:"used"`
	ResetAt  string `json:"reset_at"`
}

// Precheck 调用前检查。返回 *Exceeded 表示应拦截；error 为 ErrUnavailable 或数据库错误（调用方都应拒绝调用）。
// 仅告警型预算不拦截。没有适用预算时不访问 Valkey。
func (s *Service) Precheck(ctx context.Context, sub Subject) (*Exceeded, error) {
	bs, err := s.Applicable(ctx, sub)
	if err != nil {
		return nil, err
	}
	for _, b := range bs {
		if b.Action != budget.ActionBlock {
			continue
		}
		used, err := s.Used(ctx, b)
		if err != nil {
			return nil, err
		}
		if used >= b.LimitValue {
			w := s.Window(b)
			return &Exceeded{BudgetID: b.ID, Scope: string(b.Scope), Period: string(b.Period), Metric: string(b.Metric),
				Limit: b.LimitValue, Used: used, ResetAt: w.End.Format(time.RFC3339)}, nil
		}
	}
	return nil, nil
}

// incrIfExists 计数存在才累加（不存在时由重建得到含本次的权威值，避免从 0 开始计）。
var incrIfExists = redis.NewScript(`if redis.call('EXISTS', KEYS[1]) == 1 then return redis.call('INCRBY', KEYS[1], ARGV[1]) end
return false`)

// Settle 一条 usage_record 落库后的结算：累加计数并检查阈值。调用已经发生，失败只记日志（计数会在 TTL 到期或对账时按库纠正）。
func (s *Service) Settle(ctx context.Context, rec *ent.UsageRecord) {
	if rec == nil {
		return
	}
	bs, err := s.Applicable(ctx, Subject{OrgID: rec.OrgID, UserID: rec.UserID, DeptID: rec.DepartmentID, ModelID: rec.ModelID, AgentID: rec.AgentID})
	if err != nil {
		s.log().Error("budget settle: load budgets", "err", err)
		return
	}
	for _, b := range bs {
		w := s.Window(b)
		if rec.CreatedAt.Before(w.Start) || !rec.CreatedAt.Before(w.End) {
			continue // 跨周期边界：记录属于上一周期，本期计数不累加
		}
		delta := int64(rec.TotalTokens)
		if b.Metric == budget.MetricCost {
			delta = rec.CostMicros
		}
		k := counterKey(b, w)
		used, err := incrIfExists.Run(ctx, s.R, []string{k}, delta).Int64()
		if errors.Is(err, redis.Nil) {
			used, err = s.rebuild(ctx, b, w, k)
		}
		if err != nil {
			s.log().Error("budget settle: counter", "budget", b.ID, "err", err)
			continue
		}
		if lvl := Level(b, used); lvl > 0 {
			if err := s.alert(ctx, b, w, lvl, used); err != nil {
				s.log().Error("budget alert failed", "budget", b.ID, "err", err)
			}
		}
	}
}

// Level 已用量对应的告警级别：0 未到阈值；threshold_pct；100 已达上限。
func Level(b *ent.Budget, used int64) int {
	switch {
	case used >= b.LimitValue:
		return 100
	case used*100 >= b.LimitValue*int64(b.ThresholdPct):
		return b.ThresholdPct
	}
	return 0
}

// Recon 对账结果。
type Recon struct {
	BudgetID int    `json:"budget_id"`
	Period   string `json:"period"`
	Counter  *int64 `json:"counter"` // Valkey 中的值；nil 表示不存在
	DB       int64  `json:"db"`
	Drift    int64  `json:"drift"`
}

// Reconcile 以 usage_records 为准重置当期计数。
func (s *Service) Reconcile(ctx context.Context, b *ent.Budget) (Recon, error) {
	w := s.Window(b)
	k := counterKey(b, w)
	r := Recon{BudgetID: b.ID, Period: w.Key}
	v, err := s.R.Get(ctx, k).Int64()
	switch {
	case err == nil:
		r.Counter = &v
	case !errors.Is(err, redis.Nil):
		return r, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	n, err := s.DBUsed(ctx, b, w)
	if err != nil {
		return r, err
	}
	r.DB = n
	if r.Counter != nil {
		r.Drift = *r.Counter - n
	}
	if err := s.R.Set(ctx, k, n, counterTTL).Err(); err != nil {
		return r, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return r, nil
}

func itoa(i int) string { return strconv.Itoa(i) }
