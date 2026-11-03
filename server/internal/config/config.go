// Package config 从环境变量加载配置（前缀 HENGDESK_）。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config 是全局配置。
type Config struct {
	Env           string // dev / prod
	HTTPAddr      string
	LogLevel      string
	DatabaseURL   string
	RedisAddr     string
	S3Endpoint    string
	S3Region      string
	S3Bucket      string
	S3AccessKey   string
	S3SecretKey   string
	S3PathStyle   bool
	SessionTTL    time.Duration
	SessionIdle   time.Duration
	LockoutMax    int
	LockoutFor    time.Duration
	AdminEmail    string
	AdminPassword string
	CookieSecure  bool
	BackupDir     string
}

func get(k, def string) string {
	if v, ok := os.LookupEnv("HENGDESK_" + k); ok && v != "" {
		return v
	}
	return def
}

// Load 读取环境变量。
func Load() (*Config, error) {
	ttl, err := time.ParseDuration(get("SESSION_TTL", "12h"))
	if err != nil {
		return nil, fmt.Errorf("HENGDESK_SESSION_TTL: %w", err)
	}
	idle, err := time.ParseDuration(get("SESSION_IDLE_TIMEOUT", "2h"))
	if err != nil {
		return nil, fmt.Errorf("HENGDESK_SESSION_IDLE_TIMEOUT: %w", err)
	}
	lockFor, err := time.ParseDuration(get("LOGIN_LOCKOUT_DURATION", "15m"))
	if err != nil {
		return nil, fmt.Errorf("HENGDESK_LOGIN_LOCKOUT_DURATION: %w", err)
	}
	lockMax, err := strconv.Atoi(get("LOGIN_MAX_FAILURES", "5"))
	if err != nil || lockMax < 0 {
		return nil, fmt.Errorf("HENGDESK_LOGIN_MAX_FAILURES 必须是非负整数（0 表示关闭锁定）")
	}
	secure, _ := strconv.ParseBool(get("COOKIE_SECURE", "false"))
	pathStyle, _ := strconv.ParseBool(get("S3_PATH_STYLE", "true"))
	c := &Config{
		Env:           get("ENV", "dev"),
		HTTPAddr:      get("HTTP_ADDR", ":8080"),
		LogLevel:      get("LOG_LEVEL", "info"),
		DatabaseURL:   get("DATABASE_URL", "postgres://hengdesk:hengdesk@localhost:5432/hengdesk?sslmode=disable"),
		RedisAddr:     get("REDIS_ADDR", "localhost:6379"),
		S3Endpoint:    get("S3_ENDPOINT", "http://localhost:8333"),
		S3Region:      get("S3_REGION", "us-east-1"),
		S3Bucket:      get("S3_BUCKET", "hengdesk"),
		S3PathStyle:   pathStyle,
		S3AccessKey:   get("S3_ACCESS_KEY", ""),
		S3SecretKey:   get("S3_SECRET_KEY", ""),
		SessionTTL:    ttl,
		SessionIdle:   idle,
		LockoutMax:    lockMax,
		LockoutFor:    lockFor,
		AdminEmail:    get("ADMIN_EMAIL", "admin@hengdesk.local"),
		AdminPassword: get("ADMIN_PASSWORD", ""),
		CookieSecure:  secure,
		BackupDir:     get("BACKUP_DIR", "/backups"),
	}
	return c, nil
}
