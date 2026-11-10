// Package usage 模型调用用量：写入 usage_records、按价格表计费、按人 / 部门 / 模型 / 智能体汇总。
// 这是 PR3 预算与配额的数据源：预算检查直接复用 Summary（或同样的过滤条件）读取已用金额 / token。
//
// 金额单位为「微元」（1 元 = 1e6 微元），价格为「每百万 token 的微元数」，全程整数运算。
// estimated=true 的记录表示上游未返回 usage（或流中断），token 与金额都是网关估算值，
// 汇总结果单独给出估算部分（EstimatedCalls / EstimatedTokens / EstimatedCostMicros），界面上要标注出来。
package usage

import (
	"context"
	"errors"
	"time"

	"entgo.io/ent/dialect/sql"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/membership"
	"github.com/SSStq1314/hengdesk/server/internal/ent/predicate"
	"github.com/SSStq1314/hengdesk/server/internal/ent/usagerecord"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
)

// Price 模型价格（每百万 token，微元）。
type Price struct {
	InputMicros  int64  `json:"price_input_micros"`
	CachedMicros int64  `json:"price_cached_micros"`
	OutputMicros int64  `json:"price_output_micros"`
	Currency     string `json:"currency"`
}

// PriceOf 读取模型上的价格表。
func PriceOf(m *ent.Model) Price {
	return Price{InputMicros: m.PriceInputMicros, CachedMicros: m.PriceCachedMicros, OutputMicros: m.PriceOutputMicros, Currency: m.Currency}
}

// Cost 计算一次调用的金额（微元，四舍五入）：
//
//	(未命中缓存的输入 × 输入价 + 命中缓存的输入 × 缓存价 + 输出 × 输出价) / 1e6
//
// 输出 token（completion_tokens）已包含思考 token（reasoning_tokens），不重复计费。
func Cost(u gateway.Usage, p Price) int64 {
	cached := int64(u.CachedTokens)
	prompt := int64(u.PromptTokens)
	if cached > prompt {
		cached = prompt
	}
	num := (prompt-cached)*p.InputMicros + cached*p.CachedMicros + int64(u.CompletionTokens)*p.OutputMicros
	return (num + 500_000) / 1_000_000
}

// Record 一次调用的用量。
type Record struct {
	OrgID      int
	UserID     int
	DeptID     *int
	Model      *ent.Model
	ProviderID int
	RequestID  string
	Purpose    string // chat / test / embedding
	Stream     bool
	ErrorCode  string // 为空表示成功
	Attempt    int    // 同一请求内的第几次尝试（0 视为 1）
	Usage      gateway.Usage
	Latency    time.Duration
	// FallbackFrom 切换到备用模型时的原模型名（D-39 第 7 条）；Model 是实际使用的模型。
	FallbackFrom string
	// AgentID 智能体会话产生的用量（M1 第六块 PR3）；非智能体调用为 nil。
	AgentID *int
}

// PrimaryDept 用户的主部门（没有主部门时取任意一个所属部门；都没有返回 nil）。记录时快照，之后调岗不影响历史归属。
func PrimaryDept(ctx context.Context, db *ent.Client, userID int) *int {
	ms, err := db.Membership.Query().Where(membership.UserID(userID), membership.DepartmentIDNotNil()).
		Order(ent.Desc(membership.FieldIsPrimary), ent.Asc(membership.FieldID)).Limit(1).All(ctx)
	if err != nil || len(ms) == 0 {
		return nil
	}
	return ms[0].DepartmentID
}

// Write 写入一条用量记录，返回计算出的金额（微元）。
func Write(ctx context.Context, db *ent.Client, r Record) (*ent.UsageRecord, error) {
	if r.Model == nil {
		return nil, errors.New("usage: model 为空")
	}
	p := PriceOf(r.Model)
	status := usagerecord.StatusOk
	if r.ErrorCode != "" {
		status = usagerecord.StatusError
	}
	purpose := usagerecord.PurposeChat
	switch r.Purpose {
	case "test":
		purpose = usagerecord.PurposeTest
	case "embedding":
		purpose = usagerecord.PurposeEmbedding
	}
	u := r.Usage
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return db.UsageRecord.Create().SetOrgID(r.OrgID).SetUserID(r.UserID).SetNillableDepartmentID(r.DeptID).
		SetModelID(r.Model.ID).SetModelName(r.Model.Name).SetProviderID(r.ProviderID).SetRequestID(r.RequestID).
		SetPurpose(purpose).SetStream(r.Stream).SetStatus(status).SetErrorCode(r.ErrorCode).
		SetPromptTokens(u.PromptTokens).SetCachedTokens(u.CachedTokens).SetCompletionTokens(u.CompletionTokens).
		SetReasoningTokens(u.ReasoningTokens).SetTotalTokens(u.TotalTokens).
		SetCostMicros(Cost(u, p)).SetCurrency(p.Currency).SetEstimated(u.Estimated).SetUsageUnknown(Unknown(r.ErrorCode, u)).
		SetLatencyMs(int(r.Latency.Milliseconds())).SetAttempt(max(r.Attempt, 1)).SetFallbackFrom(r.FallbackFrom).
		SetNillableAgentID(r.AgentID).Save(ctx)
}

// Unknown 失败且拿不到任何用量（超时 / 上游 5xx 等）：按 0 记账，但标记「用量未知」以便对账（D-37 第 5 条）。
// 流式中断时网关已附上部分用量（上游给过就用上游的，否则按已收到内容估算），按部分用量记账，不算未知。
func Unknown(errorCode string, u gateway.Usage) bool {
	return errorCode != "" && u.TotalTokens == 0 && u.PromptTokens == 0 && u.CompletionTokens == 0
}

// Filter 统计过滤条件。时间区间为 [From, To)；零值表示不限。
type Filter struct {
	OrgID   int
	From    time.Time
	To      time.Time
	UserID  *int
	DeptIDs []int // 任一部门（PR3 部门预算可传入部门子树）
	ModelID *int
	AgentID *int   // 智能体维度（第七块）；nil 不限
	Purpose string // 为空表示全部；chat / test / embedding
}

func (f Filter) Predicates() []predicate.UsageRecord {
	ps := []predicate.UsageRecord{usagerecord.OrgID(f.OrgID)}
	if !f.From.IsZero() {
		ps = append(ps, usagerecord.CreatedAtGTE(f.From))
	}
	if !f.To.IsZero() {
		ps = append(ps, usagerecord.CreatedAtLT(f.To))
	}
	if f.UserID != nil {
		ps = append(ps, usagerecord.UserID(*f.UserID))
	}
	if len(f.DeptIDs) > 0 {
		ps = append(ps, usagerecord.DepartmentIDIn(f.DeptIDs...))
	}
	if f.ModelID != nil {
		ps = append(ps, usagerecord.ModelID(*f.ModelID))
	}
	if f.AgentID != nil {
		ps = append(ps, usagerecord.AgentID(*f.AgentID))
	}
	if f.Purpose != "" {
		ps = append(ps, usagerecord.PurposeEQ(usagerecord.Purpose(f.Purpose)))
	}
	return ps
}

// GroupBy 汇总维度。
type GroupBy string

const (
	ByUser  GroupBy = "user"
	ByDept  GroupBy = "department"
	ByModel GroupBy = "model"
	ByAgent GroupBy = "agent"
	ByNone  GroupBy = "total"
)

// Totals 一组用量合计。
type Totals struct {
	Calls               int64 `json:"calls"`
	ErrorCalls          int64 `json:"error_calls"`
	PromptTokens        int64 `json:"prompt_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	CostMicros          int64 `json:"cost_micros"`
	EstimatedCalls      int64 `json:"estimated_calls"`
	EstimatedTokens     int64 `json:"estimated_tokens"`
	EstimatedCostMicros int64 `json:"estimated_cost_micros"`
	// UnknownCalls 用量未知（失败且上游未返回用量，按 0 记账）的调用数。
	UnknownCalls int64 `json:"unknown_calls"`
}

// Row 汇总行。Key 为 user_id / department_id / model_id（部门为空时 Key=0 表示「无部门」）。
type Row struct {
	Key int `json:"key"`
	Totals
}

type aggRow struct {
	UserID     sql.NullInt64 `json:"user_id"`
	DeptID     sql.NullInt64 `json:"department_id"`
	ModelID    sql.NullInt64 `json:"model_id"`
	AgentID    sql.NullInt64 `json:"agent_id"`
	Estimated  bool          `json:"estimated"`
	Unknown    bool          `json:"usage_unknown"`
	Status     string        `json:"status"`
	Calls      int64         `json:"calls"`
	Prompt     sql.NullInt64 `json:"prompt"`
	Cached     sql.NullInt64 `json:"cached"`
	Completion sql.NullInt64 `json:"completion"`
	Reasoning  sql.NullInt64 `json:"reasoning"`
	Total      sql.NullInt64 `json:"total"`
	Cost       sql.NullInt64 `json:"cost"`
}

func (r aggRow) key(by GroupBy) int {
	switch by {
	case ByUser:
		return int(r.UserID.Int64)
	case ByDept:
		return int(r.DeptID.Int64)
	case ByModel:
		return int(r.ModelID.Int64)
	case ByAgent:
		return int(r.AgentID.Int64)
	}
	return 0
}

// Summary 按维度汇总。结果按金额降序、再按 token 降序。
func Summary(ctx context.Context, db *ent.Client, by GroupBy, f Filter) ([]Row, Totals, error) {
	fields := []string{usagerecord.FieldEstimated, usagerecord.FieldUsageUnknown, usagerecord.FieldStatus}
	switch by {
	case ByUser:
		fields = append(fields, usagerecord.FieldUserID)
	case ByDept:
		fields = append(fields, usagerecord.FieldDepartmentID)
	case ByModel:
		fields = append(fields, usagerecord.FieldModelID)
	case ByAgent:
		fields = append(fields, usagerecord.FieldAgentID)
	case ByNone, "":
		by = ByNone
	default:
		return nil, Totals{}, ErrBadGroup
	}
	var rows []aggRow
	err := db.UsageRecord.Query().Where(f.Predicates()...).GroupBy(fields[0], fields[1:]...).Aggregate(
		ent.As(ent.Count(), "calls"),
		ent.As(ent.Sum(usagerecord.FieldPromptTokens), "prompt"),
		ent.As(ent.Sum(usagerecord.FieldCachedTokens), "cached"),
		ent.As(ent.Sum(usagerecord.FieldCompletionTokens), "completion"),
		ent.As(ent.Sum(usagerecord.FieldReasoningTokens), "reasoning"),
		ent.As(ent.Sum(usagerecord.FieldTotalTokens), "total"),
		ent.As(ent.Sum(usagerecord.FieldCostMicros), "cost"),
	).Scan(ctx, &rows)
	if err != nil {
		return nil, Totals{}, err
	}
	idx := map[int]*Row{}
	order := []int{}
	var all Totals
	for _, r := range rows {
		k := r.key(by)
		row, ok := idx[k]
		if !ok {
			row = &Row{Key: k}
			idx[k] = row
			order = append(order, k)
		}
		for _, t := range []*Totals{&row.Totals, &all} {
			t.Calls += r.Calls
			if r.Status == string(usagerecord.StatusError) {
				t.ErrorCalls += r.Calls
			}
			t.PromptTokens += r.Prompt.Int64
			t.CachedTokens += r.Cached.Int64
			t.CompletionTokens += r.Completion.Int64
			t.ReasoningTokens += r.Reasoning.Int64
			t.TotalTokens += r.Total.Int64
			t.CostMicros += r.Cost.Int64
			if r.Unknown {
				t.UnknownCalls += r.Calls
			}
			if r.Estimated {
				t.EstimatedCalls += r.Calls
				t.EstimatedTokens += r.Total.Int64
				t.EstimatedCostMicros += r.Cost.Int64
			}
		}
	}
	out := make([]Row, 0, len(order))
	for _, k := range order {
		out = append(out, *idx[k])
	}
	sortRows(out)
	return out, all, nil
}

func sortRows(rs []Row) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && less(rs[j], rs[j-1]); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

func less(a, b Row) bool {
	if a.CostMicros != b.CostMicros {
		return a.CostMicros > b.CostMicros
	}
	if a.TotalTokens != b.TotalTokens {
		return a.TotalTokens > b.TotalTokens
	}
	return a.Key < b.Key
}

// ErrBadGroup 不支持的分组维度。
var ErrBadGroup = errors.New("usage: 不支持的分组维度")

// List 按时间倒序返回明细，最多 limit 条。
func List(ctx context.Context, db *ent.Client, f Filter, limit int) ([]*ent.UsageRecord, error) {
	return db.UsageRecord.Query().Where(f.Predicates()...).Order(ent.Desc(usagerecord.FieldCreatedAt), ent.Desc(usagerecord.FieldID)).
		Limit(limit).All(ctx)
}
