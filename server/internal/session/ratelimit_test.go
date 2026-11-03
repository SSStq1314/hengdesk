package session

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRateLimiterSlidingWindow(t *testing.T) {
	mr := miniredis.RunT(t)
	l := &RateLimiter{R: redis.NewClient(&redis.Options{Addr: mr.Addr()}), Limit: 3, Window: time.Minute}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if ok, _, err := l.Allow(ctx, "k"); !ok || err != nil {
			t.Fatalf("attempt %d blocked: %v", i, err)
		}
	}
	ok, wait, err := l.Allow(ctx, "k")
	if ok || err != nil || wait <= 0 || wait > time.Minute {
		t.Fatalf("4th: ok=%v wait=%v err=%v", ok, wait, err)
	}
	if ok, _, _ := l.Allow(ctx, "other"); !ok {
		t.Fatal("keys must be independent")
	}
	var off *RateLimiter
	if ok, _, _ := off.Allow(ctx, "k"); !ok {
		t.Fatal("nil limiter must allow")
	}
}
