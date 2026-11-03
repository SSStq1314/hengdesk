package egress

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/egresslog"
)

// DBPolicy 从 egress_rules 读取白名单，缓存 TTL（默认 5 秒）；管理端改动后调用 Invalidate 立即生效。
type DBPolicy struct {
	DB  *ent.Client
	TTL time.Duration
	// OnChange 白名单变化时回调（用于关闭空闲连接）。
	OnChange func()

	mu    sync.Mutex
	rules []Rule
	at    time.Time
}

func (p *DBPolicy) Rules(ctx context.Context) ([]Rule, error) {
	ttl := p.TTL
	if ttl == 0 {
		ttl = 5 * time.Second
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.at.IsZero() && time.Since(p.at) < ttl {
		return p.rules, nil
	}
	rows, err := p.DB.EgressRule.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rows))
	for _, r := range rows {
		rule, err := ParseRule(string(r.Kind), r.Value, r.Ports)
		if err != nil {
			continue // 库里的非法条目不生效（fail-closed）
		}
		out = append(out, rule)
	}
	p.rules, p.at = out, time.Now()
	return out, nil
}

// Invalidate 清缓存并关闭空闲连接。
func (p *DBPolicy) Invalidate() {
	p.mu.Lock()
	p.at = time.Time{}
	p.mu.Unlock()
	if p.OnChange != nil {
		p.OnChange()
	}
}

// DBSink 写 egress_logs。
type DBSink struct{ DB *ent.Client }

// Start 请求发出前写入「进行中」的放行记录。
func (s DBSink) Start(ctx context.Context, r Record) (int, error) {
	q := s.DB.EgressLog.Create().SetTs(r.TS).SetActorEmail(r.Caller.Email).SetModule(r.Caller.Module).
		SetMethod(r.Method).SetScheme(r.Scheme).SetHost(r.Host).SetPort(r.Port).SetPath(r.Path).
		SetDecision(egresslog.DecisionAllow).SetBytesOut(r.BytesOut).SetError(r.Error)
	if r.Caller.UserID != 0 {
		q.SetUserID(r.Caller.UserID)
	}
	row, err := q.Save(ctx)
	if err != nil {
		return 0, err
	}
	return row.ID, nil
}

func (s DBSink) Write(ctx context.Context, r Record) error {
	decision := "deny"
	if r.Allowed {
		decision = "allow"
	}
	if r.ID > 0 {
		return s.DB.EgressLog.UpdateOneID(r.ID).SetIP(r.IP).SetDecision(egresslog.Decision(decision)).SetReason(r.Reason).
			SetStatus(r.Status).SetDurationMs(r.DurationMS).SetBytesOut(r.BytesOut).SetBytesIn(r.BytesIn).SetError(r.Error).Exec(ctx)
	}
	q := s.DB.EgressLog.Create().SetTs(r.TS).SetActorEmail(r.Caller.Email).SetModule(r.Caller.Module).
		SetMethod(r.Method).SetScheme(r.Scheme).SetHost(r.Host).SetPort(r.Port).SetPath(r.Path).SetIP(r.IP).
		SetDecision(egresslog.Decision(decision)).SetReason(r.Reason).SetStatus(r.Status).SetDurationMs(r.DurationMS).
		SetBytesOut(r.BytesOut).SetBytesIn(r.BytesIn).SetError(r.Error)
	if r.Caller.UserID != 0 {
		q.SetUserID(r.Caller.UserID)
	}
	return q.Exec(ctx)
}

// New 生产用出站客户端：白名单读库、日志写 egress_logs、拒绝写审计。
func New(db *ent.Client, w audit.Writer, lg *slog.Logger) (*Client, *DBPolicy) {
	p := &DBPolicy{DB: db}
	c := &Client{Policy: p, Sink: DBSink{DB: db}, Audit: w, Log: lg}
	p.OnChange = c.CloseIdle
	return c, p
}
