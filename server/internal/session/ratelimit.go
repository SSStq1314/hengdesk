package session

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter 基于 Valkey 有序集合的滑动窗口限速。Limit<=0 表示关闭。
type RateLimiter struct {
	R      redis.UniversalClient
	Limit  int
	Window time.Duration
	Prefix string // 默认 "hd:rl:"
}

// Allow 记录一次尝试并判断是否超限；超限时返回需等待的时长。
func (l *RateLimiter) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	if l == nil || l.Limit <= 0 {
		return true, 0, nil
	}
	p := l.Prefix
	if p == "" {
		p = "hd:rl:"
	}
	k := p + key
	now := time.Now()
	floor := strconv.FormatInt(now.Add(-l.Window).UnixMicro(), 10)
	pipe := l.R.TxPipeline()
	pipe.ZRemRangeByScore(ctx, k, "-inf", "("+floor)
	pipe.ZAdd(ctx, k, redis.Z{Score: float64(now.UnixMicro()), Member: fmt.Sprintf("%d-%d", now.UnixNano(), rand.Uint32())})
	cnt := pipe.ZCard(ctx, k)
	oldest := pipe.ZRangeWithScores(ctx, k, 0, 0)
	pipe.PExpire(ctx, k, l.Window)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, 0, err
	}
	if int(cnt.Val()) <= l.Limit {
		return true, 0, nil
	}
	wait := l.Window
	if o := oldest.Val(); len(o) > 0 {
		wait = time.UnixMicro(int64(o[0].Score)).Add(l.Window).Sub(now)
	}
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait, nil
}
