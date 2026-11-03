package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newRedis(t *testing.T) (*miniredis.Miniredis, redis.UniversalClient) {
	m := miniredis.RunT(t)
	return m, redis.NewClient(&redis.Options{Addr: m.Addr()})
}

func TestSessionLifecycle(t *testing.T) {
	m, r := newRedis(t)
	ctx := context.Background()
	s := &Store{R: r, TTL: time.Hour, IdleTimeout: 10 * time.Minute}
	tok, ss, err := s.Create(ctx, 7, "1.1.1.1", "ua")
	if err != nil || ss.CSRF == "" {
		t.Fatal(err)
	}
	for _, k := range m.Keys() {
		if k == "hd:sess:"+tok {
			t.Fatal("raw token stored as key")
		}
	}
	if got, err := s.Get(ctx, tok); err != nil || got.UserID != 7 {
		t.Fatalf("get: %v", err)
	}
	m.FastForward(11 * time.Minute)
	if _, err := s.Get(ctx, tok); !errors.Is(err, ErrNotFound) {
		t.Fatalf("idle expiry: %v", err)
	}
	t1, _, _ := s.Create(ctx, 7, "", "")
	t2, _, _ := s.Create(ctx, 7, "", "")
	other, _, _ := s.Create(ctx, 8, "", "")
	if n, _ := s.Count(ctx, 7); n != 2 {
		t.Fatalf("count %d", n)
	}
	n, err := s.RevokeUser(ctx, 7)
	if err != nil || n != 2 {
		t.Fatalf("revoke %d %v", n, err)
	}
	for _, x := range []string{t1, t2} {
		if _, err := s.Get(ctx, x); !errors.Is(err, ErrNotFound) {
			t.Fatal("not revoked")
		}
	}
	if _, err := s.Get(ctx, other); err != nil {
		t.Fatal("other user affected")
	}
	if err := s.Delete(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Fatal("logout failed")
	}
}

func TestLockout(t *testing.T) {
	m, r := newRedis(t)
	ctx := context.Background()
	l := &Lockout{R: r, MaxFailures: 5, Duration: 15 * time.Minute}
	for i := 0; i < 4; i++ {
		if locked, _ := l.Fail(ctx, "A@x"); locked {
			t.Fatalf("locked early at %d", i)
		}
	}
	if d, _ := l.Locked(ctx, "a@x"); d != 0 {
		t.Fatal("locked before 5")
	}
	if locked, _ := l.Fail(ctx, "a@x"); !locked {
		t.Fatal("not locked at 5")
	}
	if d, _ := l.Locked(ctx, "a@x"); d <= 14*time.Minute {
		t.Fatalf("lock duration %v", d)
	}
	m.FastForward(15*time.Minute + time.Second)
	if d, _ := l.Locked(ctx, "a@x"); d != 0 {
		t.Fatal("lock not released")
	}
	_, _ = l.Fail(ctx, "a@x")
	_ = l.Reset(ctx, "a@x")
	for i := 0; i < 4; i++ {
		_, _ = l.Fail(ctx, "a@x")
	}
	if d, _ := l.Locked(ctx, "a@x"); d != 0 {
		t.Fatal("reset did not clear counter")
	}
	off := &Lockout{R: r}
	if locked, _ := off.Fail(ctx, "b"); locked {
		t.Fatal("disabled lockout locked")
	}
}
