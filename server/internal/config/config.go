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
	JWTSecret     string
	SessionTTL    time.Duration
	AdminEmail    string
	AdminPassword string
	CookieSecure  bool
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
		JWTSecret:     get("JWT_SECRET", ""),
		SessionTTL:    ttl,
		AdminEmail:    get("ADMIN_EMAIL", "admin@hengdesk.local"),
		AdminPassword: get("ADMIN_PASSWORD", ""),
		CookieSecure:  secure,
	}
	if c.Env == "prod" && len(c.JWTSecret) < 32 {
		return nil, fmt.Errorf("生产环境必须设置至少 32 字节的 HENGDESK_JWT_SECRET")
	}
	if c.JWTSecret == "" {
		c.JWTSecret = "dev-insecure-secret-change-me-0000000000"
	}
	return c, nil
}
