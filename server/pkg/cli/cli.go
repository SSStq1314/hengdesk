// Package cli 是衡台单二进制的命令行实现（server / worker / migrate）。
//
// 社区版入口 cmd/hengdesk 直接调用 Main；专业版在独立闭源 module 中先 import 自己的扩展包
// （其 init 调用 edition.Register），再调用 Main。社区版代码不依赖专业版。
package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/egress"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auditarchive"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/backup"
	"github.com/SSStq1314/hengdesk/server/internal/chatretention"
	"github.com/SSStq1314/hengdesk/server/internal/config"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/httpserver"
	hlog "github.com/SSStq1314/hengdesk/server/internal/log"
	"github.com/SSStq1314/hengdesk/server/internal/objstore"
	"github.com/SSStq1314/hengdesk/server/internal/queue"
	"github.com/SSStq1314/hengdesk/server/internal/session"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

// Main 执行命令行。
func Main() {
	root := &cobra.Command{Use: "hengdesk", Short: "衡台 HengDesk 企业 AI 办公平台", SilenceUsage: true}
	root.AddCommand(serverCmd(), workerCmd(), migrateCmd(), auditCmd(), keysCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func signalCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func serverCmd() *cobra.Command {
	return &cobra.Command{Use: "server", Short: "启动 HTTP API 服务", RunE: func(_ *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		lg := hlog.New(cfg.LogLevel)
		client, _, err := store.Open(cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		e := httpserver.New(lg)
		e.IPExtractor = httpserver.IPExtractor(cfg.TrustedProxies)
		w := audit.EntWriter{C: client}
		rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
		defer func() { _ = rdb.Close() }()
		// FR-27：统一出站客户端。升级迁移（仅一次）把已有上游主机写入白名单并审计。
		eg, egPolicy := egress.New(client, w, lg)
		if n, err := egress.Seed(context.Background(), client, w, nil, cfg.OIDCIssuer); err != nil {
			return fmt.Errorf("egress upgrade seed: %w", err)
		} else if n > 0 {
			lg.Warn("egress allowlist seeded from existing upstreams; review it in 出站控制", "rules", n)
		}
		// D-38 第 3 条：端口留空改为仅 443，升级时修正已有的空端口条目（仅一次，逐条审计）。
		if n, err := egress.MigratePorts(context.Background(), client, w, nil, cfg.OIDCIssuer); err != nil {
			return fmt.Errorf("egress ports migrate: %w", err)
		} else if n > 0 {
			lg.Warn("egress allowlist ports migrated for D-38 (empty now means 443 only); review entries marked * in 出站控制", "rules", n)
		}
		// D-39 第 1 条：#31 改成 * 的手工条目收窄为 443 / 实际端口，标记待复核（仅一次，逐条审计）。
		if n, err := egress.MigrateD39Ports(context.Background(), client, w, nil, cfg.OIDCIssuer); err != nil {
			return fmt.Errorf("egress d39 ports migrate: %w", err)
		} else if n > 0 {
			lg.Warn("egress allowlist: manual entries widened to * by D-38 were narrowed to 443 (D-39); review entries marked 待复核 and check 被拦截的出站请求 in 出站控制", "rules", n)
		}
		var oidcSvc *auth.OIDC
		if cfg.OIDCIssuer != "" {
			oidcSvc = &auth.OIDC{R: rdb, Cfg: auth.OIDCConfig{Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID,
				ClientSecret: cfg.OIDCClientSecret, RedirectURL: cfg.OIDCRedirectURL, Scopes: cfg.OIDCScopes,
				LinkBy: cfg.OIDCLinkBy, AutoCreate: cfg.OIDCAutoCreate, OrgSlug: "default", DisplayName: cfg.OIDCDisplayName,
				EmailDomains: cfg.OIDCEmailDomains, DeptClaim: cfg.OIDCDeptClaim, DeptMatch: cfg.OIDCDeptMatch, DeptDefault: cfg.OIDCDeptDefault, HTTPClient: eg.HTTP()}}
			lg.Info("oidc enabled", "issuer", cfg.OIDCIssuer, "link_by", cfg.OIDCLinkBy, "auto_create", cfg.OIDCAutoCreate,
				"auto_create_effective", cfg.OIDCAutoCreate && len(cfg.OIDCEmailDomains) > 0, "dept_claim", cfg.OIDCDeptClaim, "enforce", cfg.OIDCEnforce)
		}
		keys, err := gateway.LoadKeyring(cfg.MasterKeyFile, cfg.MasterKey)
		if err != nil {
			return masterKeyLoadError(cfg.MasterKeyFile, err)
		}
		// D-36 启动自检：有密文却没有 / 解不开主密钥时明确失败，而不是带病启动。
		if err := gateway.SelfCheck(context.Background(), client, keys, cfg.Env == "prod"); err != nil {
			return err
		}
		if keys == nil {
			lg.Warn("model gateway master key not configured; upstream API keys cannot be stored or used")
		} else {
			lg.Info("model gateway master key loaded", "file", cfg.MasterKeyFile, "fingerprint", keys.Fingerprint())
		}
		gw := &gateway.Service{DB: client, Keys: keys, Client: &gateway.Client{HTTP: eg.HTTP()}}
		var files api.FileStore
		if cfg.S3AccessKey != "" {
			st, err := objstore.New(objstore.Config{Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, AccessKey: cfg.S3AccessKey,
				SecretKey: cfg.S3SecretKey, Bucket: cfg.S3Bucket, PathStyle: cfg.S3PathStyle})
			if err != nil {
				return fmt.Errorf("对象存储配置错误: %w", err)
			}
			files = st
		} else {
			lg.Warn("object storage not configured; conversation file upload disabled")
		}
		api.Mount(e, api.Deps{Files: files, FileLimits: api.FileLimits{MaxBytes: int64(cfg.ChatFileMaxMB) << 20}, Timezone: cfg.Timezone, Gateway: gw, Egress: eg, EgressPolicy: egPolicy, DB: client, Audit: w, Log: lg, Auth: &auth.Service{OIDC: oidcSvc, EnforceSSO: cfg.OIDCEnforce, BreakGlass: cfg.OIDCBreakGlass,
			DB: client, Audit: w, Secure: cfg.CookieSecure,
			Sessions:  &session.Store{R: rdb, TTL: cfg.SessionTTL, IdleTimeout: cfg.SessionIdle},
			Lockout:   &session.Lockout{R: rdb, MaxFailures: cfg.LockoutMax, Duration: cfg.LockoutFor},
			IPLimit:   &session.RateLimiter{R: rdb, Limit: cfg.LoginIPLimit, Window: cfg.LoginIPWindow},
			Passwords: auth.PasswordPolicy{MinLen: cfg.PasswordMinLen},
		}, System: api.SystemConfig{BackupDir: cfg.BackupDir, BackupKeep: cfg.BackupKeep, DiskLowPercent: cfg.DiskLowPercent,
			PublicHTTPS: cfg.PublicHTTPS, TrustedProxies: cfg.TrustedProxies, TLSCertFile: cfg.TLSCertFile,
			AuditRetentionDays: cfg.AuditRetentionDays, MasterKeyPath: masterKeyHostPath(cfg), MasterKeyFingerprint: keys.Fingerprint()}})
		ctx, stop := signalCtx()
		defer stop()
		go func() {
			lg.Info("server listening", "addr", cfg.HTTPAddr)
			if err := e.Start(cfg.HTTPAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
				lg.Error("server error", "err", err)
				stop()
			}
		}()
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return e.Shutdown(sctx)
	}}
}

func workerCmd() *cobra.Command {
	return &cobra.Command{Use: "worker", Short: "启动异步任务 worker", RunE: func(_ *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		lg := hlog.New(cfg.LogLevel)
		client, _, err := store.Open(cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		srv := asynq.NewServer(queue.RedisOpt(cfg.RedisAddr), asynq.Config{Concurrency: 10, Logger: nil})
		ctx, stop := signalCtx()
		defer stop()
		mux := queue.Mux(lg)
		// FR-40：审计保留期到期提醒（只提醒，不归档、不删除）。
		mux.HandleFunc(auditarchive.TaskRemind, func(ctx context.Context, _ *asynq.Task) error {
			sent, st, err := auditarchive.Remind(ctx, client, audit.EntWriter{C: client}, cfg.AuditRetentionDays, time.Now())
			if err != nil {
				return err
			}
			lg.Info("audit retention check", "due_soon", st.DueSoon, "days_left", st.DaysLeft, "reminded", sent)
			return nil
		})
		// FR-40 / D-40 第 2 条：对话消息保留期清理（默认不开启；开启后按保留期删除，跳过法律保留）。
		var objs chatretention.ObjectDeleter
		if cfg.S3AccessKey != "" {
			st, err := objstore.New(objstore.Config{Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, AccessKey: cfg.S3AccessKey,
				SecretKey: cfg.S3SecretKey, Bucket: cfg.S3Bucket, PathStyle: cfg.S3PathStyle})
			if err != nil {
				return fmt.Errorf("对象存储配置错误: %w", err)
			}
			objs = st
		}
		mux.HandleFunc(chatretention.TaskPurge, func(ctx context.Context, _ *asynq.Task) error {
			res, err := chatretention.Purge(ctx, client, audit.EntWriter{C: client}, objs,
				chatretention.Operator{Email: "system", Trigger: "scheduled"}, time.Now())
			if errors.Is(err, chatretention.ErrDisabled) {
				return nil
			}
			if err != nil {
				return err
			}
			lg.Info("chat retention purge", "conversations", res.Conversations, "messages", res.Messages, "files", res.Files,
				"held_orgs", res.HeldOrgs, "held_users", res.HeldUsers, "object_errors", res.ObjectErrors)
			return nil
		})
		if err := srv.Start(mux); err != nil {
			return err
		}
		sch := asynq.NewScheduler(queue.RedisOpt(cfg.RedisAddr), nil)
		if _, err := sch.Register("@every 12h", asynq.NewTask(auditarchive.TaskRemind, nil), asynq.Unique(time.Hour)); err != nil {
			return err
		}
		if _, err := sch.Register("@every 24h", asynq.NewTask(chatretention.TaskPurge, nil), asynq.Unique(time.Hour)); err != nil {
			return err
		}
		if err := sch.Start(); err != nil {
			return err
		}
		lg.Info("worker started", "redis", cfg.RedisAddr, "audit_retention_days", cfg.AuditRetentionDays)
		<-ctx.Done()
		sch.Shutdown()
		srv.Shutdown()
		return nil
	}}
}

func waitDB(ctx context.Context, db *sql.DB) error {
	for {
		if err := db.PingContext(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func migrateCmd() *cobra.Command {
	var statusOnly bool
	cmd := &cobra.Command{Use: "migrate", Short: "执行数据库迁移（升级前自动备份）并初始化管理员", RunE: func(_ *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		lg := hlog.New(cfg.LogLevel)
		client, db, err := store.Open(cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := waitDB(ctx, db); err != nil {
			return err
		}
		st, err := store.Status(ctx, db)
		if err != nil {
			return err
		}
		lg.Info("migration status", "applied", len(st.Applied), "pending", st.Pending)
		if statusOnly {
			return nil
		}
		if len(st.Pending) > 0 && !st.Fresh {
			// 已有数据的库：先备份，备份失败则中止升级。
			file, err := backup.Run(ctx, backup.PGDump{}, cfg.DatabaseURL, cfg.BackupDir, "pre-upgrade")
			if err != nil {
				lg.Error("升级前备份失败，已中止升级", "err", err)
				return fmt.Errorf("升级前备份失败，已中止升级: %w", err)
			}
			lg.Info("升级前备份完成", "file", file)
			lg.Warn("备份文件不包含主密钥：请把主密钥文件与数据库备份分开保管，丢失后已存的上游 Key 无法恢复", "backup", file)
			removed, err := backup.Prune(cfg.BackupDir, cfg.BackupKeep)
			if err != nil {
				lg.Warn("清理旧备份失败", "err", err)
			}
			lg.Info("旧备份清理", "keep", cfg.BackupKeep, "removed", len(removed))
		}
		applied, err := store.ApplyMigrations(ctx, db)
		if err != nil {
			return err
		}
		lg.Info("migrations applied", "files", applied)
		created, err := store.Seed(ctx, client, store.SeedOptions{OrgName: "默认组织", OrgSlug: "default",
			AdminEmail: cfg.AdminEmail, AdminPassword: cfg.AdminPassword})
		if err != nil {
			return err
		}
		lg.Info("migrate done", "admin_email", cfg.AdminEmail, "admin_created", created)
		audCreated, err := store.SeedAuditor(ctx, client, store.SeedOptions{OrgSlug: "default",
			AdminEmail: cfg.AdminEmail, AuditorEmail: cfg.AuditorEmail, AuditorPassword: cfg.AuditorPassword})
		if err != nil {
			return err
		}
		if cfg.AuditorEmail != "" {
			lg.Info("initial auditor", "auditor_email", cfg.AuditorEmail, "created", audCreated)
		}
		if cfg.S3AccessKey != "" {
			if err := ensureBucket(ctx, cfg); err != nil {
				return err
			}
			lg.Info("bucket ready", "bucket", cfg.S3Bucket)
		}
		return nil
	}}
	cmd.Flags().BoolVar(&statusOnly, "status", false, "只显示迁移状态，不执行")
	return cmd
}

// ensureBucket 创建默认桶，对象存储启动较慢时重试。
func ensureBucket(ctx context.Context, cfg *config.Config) error {
	st, err := objstore.New(objstore.Config{Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey, Bucket: cfg.S3Bucket, PathStyle: cfg.S3PathStyle})
	if err != nil {
		return err
	}
	for {
		if err = st.EnsureBucket(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("对象存储不可用: %w", err)
		case <-time.After(2 * time.Second):
		}
	}
}

// masterKeyHostPath 管理后台展示的主密钥位置：优先 HENGDESK_MASTER_KEY_HOST_PATH（宿主机路径，由 compose 传入），
// 其次容器内文件路径；用环境变量传入时提示迁移。
func masterKeyHostPath(cfg *config.Config) string {
	if v := os.Getenv("HENGDESK_MASTER_KEY_HOST_PATH"); v != "" {
		return v
	}
	if cfg.MasterKeyFile != "" {
		return cfg.MasterKeyFile
	}
	if cfg.MasterKey != "" {
		return "环境变量 HENGDESK_MASTER_KEY（建议迁移到 secrets/master.key）"
	}
	return ""
}
