package session

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Lockout 连续登录失败锁定：Window 内失败 MaxFailures 次即锁定 Duration。
// 按账号（小写邮箱）计数，账号不存在时同样计数，避免借此枚举账号。
type Lockout struct {
	R           redis.UniversalClient
	MaxFailures int
	Duration    time.Duration
	Prefix      string
}

func (l *Lockout) k(kind, acct string) string {
	p := l.Prefix
	if p == "" {
		p = "hd:"
	}
	return p + "login:" + kind + ":" + strings.ToLower(strings.TrimSpace(acct))
}

// Locked 返回剩余锁定时长，0 表示未锁定。
func (l *Lockout) Locked(ctx context.Context, acct string) (time.Duration, error) {
	if l.MaxFailures <= 0 {
		return 0, nil
	}
	d, err := l.R.PTTL(ctx, l.k("lock", acct)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, err
	}
	if d < 0 {
		return 0, nil
	}
	return d, nil
}

// Fail 记一次失败，返回是否因此进入锁定。
func (l *Lockout) Fail(ctx context.Context, acct string) (bool, error) {
	if l.MaxFailures <= 0 {
		return false, nil
	}
	fk := l.k("fail", acct)
	pipe := l.R.TxPipeline()
	incr := pipe.Incr(ctx, fk)
	pipe.Expire(ctx, fk, l.Duration)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	if incr.Val() >= int64(l.MaxFailures) {
		pipe := l.R.TxPipeline()
		pipe.Set(ctx, l.k("lock", acct), "1", l.Duration)
		pipe.Del(ctx, fk)
		_, err := pipe.Exec(ctx)
		return true, err
	}
	return false, nil
}

// Reset 登录成功或管理员解锁时清零。
func (l *Lockout) Reset(ctx context.Context, acct string) error {
	return l.R.Del(ctx, l.k("fail", acct), l.k("lock", acct)).Err()
}
