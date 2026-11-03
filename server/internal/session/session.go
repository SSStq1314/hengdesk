// Package session 实现基于 Valkey 的服务端会话、登录失败锁定。
//
// 会话 token 只以 SHA-256 摘要作为键存放，Valkey 中不保存原始 token。
// 每个用户维护一个会话集合，用于强制下线和改密/停用/重置密码时全量失效。
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound 会话不存在或已失效。
var ErrNotFound = errors.New("session not found")

// Session 会话数据。
type Session struct {
	UserID    int       `json:"uid"`
	CSRF      string    `json:"csrf"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"` // 绝对过期时间
	IP        string    `json:"ip"`
	UserAgent string    `json:"ua"`
}

// Store 会话存储。
type Store struct {
	R           redis.UniversalClient
	TTL         time.Duration // 绝对有效期
	IdleTimeout time.Duration // 空闲超时，0 表示与 TTL 相同
	Prefix      string
}

func (s *Store) p() string {
	if s.Prefix == "" {
		return "hd:"
	}
	return s.Prefix
}

func (s *Store) idle() time.Duration {
	if s.IdleTimeout <= 0 || s.IdleTimeout > s.TTL {
		return s.TTL
	}
	return s.IdleTimeout
}

func digest(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func (s *Store) key(tok string) string  { return s.p() + "sess:" + digest(tok) }
func (s *Store) userKey(uid int) string { return s.p() + "usess:" + strconv.Itoa(uid) }

// RandomToken 生成 32 字节随机 token（base64url）。
func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create 新建会话，返回原始 token（只出现在 cookie 中）。
func (s *Store) Create(ctx context.Context, uid int, ip, ua string) (string, *Session, error) {
	tok, err := RandomToken()
	if err != nil {
		return "", nil, err
	}
	csrf, err := RandomToken()
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC()
	ss := &Session{UserID: uid, CSRF: csrf, CreatedAt: now, ExpiresAt: now.Add(s.TTL), IP: ip, UserAgent: ua}
	b, _ := json.Marshal(ss)
	pipe := s.R.TxPipeline()
	pipe.Set(ctx, s.key(tok), b, s.idle())
	pipe.SAdd(ctx, s.userKey(uid), digest(tok))
	pipe.Expire(ctx, s.userKey(uid), s.TTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", nil, err
	}
	return tok, ss, nil
}

// Get 读取并续期（滑动空闲超时，不超过绝对过期时间）。
func (s *Store) Get(ctx context.Context, tok string) (*Session, error) {
	if tok == "" {
		return nil, ErrNotFound
	}
	b, err := s.R.Get(ctx, s.key(tok)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var ss Session
	if err := json.Unmarshal(b, &ss); err != nil {
		return nil, ErrNotFound
	}
	left := time.Until(ss.ExpiresAt)
	if left <= 0 {
		_ = s.Delete(ctx, tok)
		return nil, ErrNotFound
	}
	if ttl := s.idle(); ttl < left {
		left = ttl
	}
	s.R.Expire(ctx, s.key(tok), left)
	return &ss, nil
}

// Delete 注销单个会话。
func (s *Store) Delete(ctx context.Context, tok string) error {
	b, err := s.R.Get(ctx, s.key(tok)).Bytes()
	if err == nil {
		var ss Session
		if json.Unmarshal(b, &ss) == nil {
			s.R.SRem(ctx, s.userKey(ss.UserID), digest(tok))
		}
	}
	return s.R.Del(ctx, s.key(tok)).Err()
}

// RevokeUser 失效某用户的全部会话，返回失效数量。
func (s *Store) RevokeUser(ctx context.Context, uid int) (int, error) {
	ds, err := s.R.SMembers(ctx, s.userKey(uid)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, err
	}
	keys := []string{s.userKey(uid)}
	for _, d := range ds {
		keys = append(keys, s.p()+"sess:"+d)
	}
	n, err := s.R.Del(ctx, keys...).Result()
	if err != nil {
		return 0, err
	}
	if n > 0 {
		n-- // 不计集合本身
	}
	return int(n), nil
}

// Count 用户当前活跃会话数（清理已过期的成员）。
func (s *Store) Count(ctx context.Context, uid int) (int, error) {
	ds, err := s.R.SMembers(ctx, s.userKey(uid)).Result()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range ds {
		ok, _ := s.R.Exists(ctx, s.p()+"sess:"+d).Result()
		if ok == 1 {
			n++
		} else {
			s.R.SRem(ctx, s.userKey(uid), d)
		}
	}
	return n, nil
}
