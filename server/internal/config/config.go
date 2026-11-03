// Package config 从环境变量加载配置（前缀 HENGDESK_）。
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
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
	// AuditorEmail / AuditorPassword 可选的初始安全审计员。
	AuditorEmail    string
	AuditorPassword string
	CookieSecure    bool
	// PublicHTTPS 对外访问已是 HTTPS（默认跟随 CookieSecure）。
	PublicHTTPS bool
	BackupDir   string
	// PasswordMinLen 本地密码最小长度（默认 8，范围 8~72）。
	PasswordMinLen int
	// LoginIPLimit / LoginIPWindow 按 IP 的登录尝试滑动窗口限速（0 表示关闭）。
	LoginIPLimit  int
	LoginIPWindow time.Duration
	// TrustedProxies 允许读取 X-Forwarded-For 的上游代理网段；为空时只用 TCP 对端地址。
	TrustedProxies []*net.IPNet
	// BackupKeep 保留最近 N 份备份；DiskLowPercent 备份盘剩余空间低于该百分比时告警。
	BackupKeep     int
	DiskLowPercent int
	// OIDC 单点登录（OIDCIssuer 为空表示未启用）。
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	OIDCScopes       []string
	OIDCLinkBy       string // email / sub
	OIDCAutoCreate   bool   // 默认 false
	OIDCDisplayName  string
	// OIDCEmailDomains 邮箱域名白名单（D-34）：为空时不自动建号、也不按邮箱关联。
	OIDCEmailDomains []string
	// OIDCDeptClaim 部门 claim 名；为空不映射。OIDCDeptMatch：code（匹配部门 external_id）或 path（部门名路径，如 研发部/后端组）。
	OIDCDeptClaim   string
	OIDCDeptMatch   string
	OIDCDeptDefault string // 映射不到时归入的部门（按 OIDCDeptMatch 的方式书写）；为空则不归入部门
	// OIDCEnforce 强制 SSO：开启后仅 OIDCBreakGlass 中的紧急管理员（最多 2 个）可用密码登录。
	OIDCEnforce    bool
	OIDCBreakGlass []string
}

// DefaultTrustedProxies 默认信任回环与私有网段（Compose 内 nginx 反代）。
const DefaultTrustedProxies = "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"

func intEnv(k, def string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(get(k, def))
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("HENGDESK_%s 必须是 %d~%d 的整数", k, lo, hi)
	}
	return n, nil
}

// ParseCIDRs 解析逗号分隔的 CIDR 或单个 IP。
func ParseCIDRs(s string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" || f == "none" {
			continue
		}
		if !strings.Contains(f, "/") {
			if ip := net.ParseIP(f); ip != nil && ip.To4() != nil {
				f += "/32"
			} else {
				f += "/128"
			}
		}
		_, n, err := net.ParseCIDR(f)
		if err != nil {
			return nil, fmt.Errorf("HENGDESK_TRUSTED_PROXIES: %w", err)
		}
		out = append(out, n)
	}
	return out, nil
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
	minLen, err := intEnv("PASSWORD_MIN_LENGTH", "8", 8, 72)
	if err != nil {
		return nil, err
	}
	ipLimit, err := intEnv("LOGIN_IP_LIMIT", "30", 0, 1000000)
	if err != nil {
		return nil, err
	}
	ipWin, err := time.ParseDuration(get("LOGIN_IP_WINDOW", "10m"))
	if err != nil || ipWin <= 0 {
		return nil, fmt.Errorf("HENGDESK_LOGIN_IP_WINDOW 必须是正的时长")
	}
	proxies, err := ParseCIDRs(get("TRUSTED_PROXIES", DefaultTrustedProxies))
	if err != nil {
		return nil, err
	}
	keep, err := intEnv("BACKUP_KEEP", "5", 1, 1000)
	if err != nil {
		return nil, err
	}
	low, err := intEnv("DISK_LOW_PERCENT", "10", 0, 99)
	if err != nil {
		return nil, err
	}
	secure, _ := strconv.ParseBool(get("COOKIE_SECURE", "false"))
	publicHTTPS, err := strconv.ParseBool(get("PUBLIC_HTTPS", strconv.FormatBool(secure)))
	if err != nil {
		return nil, fmt.Errorf("HENGDESK_PUBLIC_HTTPS 必须是 true / false")
	}
	pathStyle, _ := strconv.ParseBool(get("S3_PATH_STYLE", "true"))
	autoCreate, _ := strconv.ParseBool(get("OIDC_AUTO_CREATE", "true"))
	scopes := strings.Fields(strings.ReplaceAll(get("OIDC_SCOPES", "openid email profile"), ",", " "))
	c := &Config{
		Env:              get("ENV", "dev"),
		HTTPAddr:         get("HTTP_ADDR", ":8080"),
		LogLevel:         get("LOG_LEVEL", "info"),
		DatabaseURL:      get("DATABASE_URL", "postgres://hengdesk:hengdesk@localhost:5432/hengdesk?sslmode=disable"),
		RedisAddr:        get("REDIS_ADDR", "localhost:6379"),
		S3Endpoint:       get("S3_ENDPOINT", "http://localhost:8333"),
		S3Region:         get("S3_REGION", "us-east-1"),
		S3Bucket:         get("S3_BUCKET", "hengdesk"),
		S3PathStyle:      pathStyle,
		S3AccessKey:      get("S3_ACCESS_KEY", ""),
		S3SecretKey:      get("S3_SECRET_KEY", ""),
		SessionTTL:       ttl,
		SessionIdle:      idle,
		LockoutMax:       lockMax,
		LockoutFor:       lockFor,
		AdminEmail:       get("ADMIN_EMAIL", "admin@hengdesk.local"),
		AdminPassword:    get("ADMIN_PASSWORD", ""),
		AuditorEmail:     get("AUDITOR_EMAIL", ""),
		AuditorPassword:  get("AUDITOR_PASSWORD", ""),
		CookieSecure:     secure,
		PublicHTTPS:      publicHTTPS,
		BackupDir:        get("BACKUP_DIR", "/backups"),
		PasswordMinLen:   minLen,
		LoginIPLimit:     ipLimit,
		LoginIPWindow:    ipWin,
		TrustedProxies:   proxies,
		BackupKeep:       keep,
		DiskLowPercent:   low,
		OIDCIssuer:       get("OIDC_ISSUER", ""),
		OIDCClientID:     get("OIDC_CLIENT_ID", ""),
		OIDCClientSecret: get("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:  get("OIDC_REDIRECT_URL", ""),
		OIDCScopes:       scopes,
		OIDCLinkBy:       get("OIDC_LINK_BY", "email"),
		OIDCAutoCreate:   autoCreate,
		OIDCDisplayName:  get("OIDC_DISPLAY_NAME", "企业单点登录"),
		OIDCEmailDomains: lowerList(get("OIDC_EMAIL_DOMAINS", "")),
		OIDCDeptClaim:    get("OIDC_DEPT_CLAIM", ""),
		OIDCDeptMatch:    get("OIDC_DEPT_MATCH", "path"),
		OIDCDeptDefault:  get("OIDC_DEPT_DEFAULT", ""),
		OIDCBreakGlass:   lowerList(get("OIDC_BREAKGLASS_EMAILS", "")),
	}
	enforce, err := strconv.ParseBool(get("OIDC_ENFORCE", "false"))
	if err != nil {
		return nil, fmt.Errorf("HENGDESK_OIDC_ENFORCE 必须是 true / false")
	}
	c.OIDCEnforce = enforce
	if err := c.validateOIDC(); err != nil {
		return nil, err
	}
	return c, nil
}

// MaxBreakGlass 强制 SSO 下紧急管理员账号上限（D-34）。
const MaxBreakGlass = 2

func lowerList(v string) []string {
	var out []string
	for _, x := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		out = append(out, strings.ToLower(strings.TrimPrefix(x, "@")))
	}
	return out
}

func (c *Config) validateOIDC() error {
	if c.OIDCDeptMatch != "path" && c.OIDCDeptMatch != "code" {
		return fmt.Errorf("HENGDESK_OIDC_DEPT_MATCH 只能是 path 或 code")
	}
	if len(c.OIDCBreakGlass) > MaxBreakGlass {
		return fmt.Errorf("HENGDESK_OIDC_BREAKGLASS_EMAILS 最多 %d 个，当前 %d 个", MaxBreakGlass, len(c.OIDCBreakGlass))
	}
	if c.OIDCEnforce {
		if c.OIDCIssuer == "" {
			return fmt.Errorf("HENGDESK_OIDC_ENFORCE=true 需要先配置 HENGDESK_OIDC_ISSUER")
		}
		if len(c.OIDCBreakGlass) == 0 {
			return fmt.Errorf("HENGDESK_OIDC_ENFORCE=true 时须在 HENGDESK_OIDC_BREAKGLASS_EMAILS 配置 1～2 个紧急管理员")
		}
	}
	if c.OIDCIssuer != "" {
		if c.OIDCClientID == "" || c.OIDCRedirectURL == "" {
			return fmt.Errorf("启用 OIDC 需同时设置 HENGDESK_OIDC_CLIENT_ID 与 HENGDESK_OIDC_REDIRECT_URL")
		}
		if c.OIDCLinkBy != "email" && c.OIDCLinkBy != "sub" {
			return fmt.Errorf("HENGDESK_OIDC_LINK_BY 只能是 email 或 sub")
		}
	}
	return nil
}
