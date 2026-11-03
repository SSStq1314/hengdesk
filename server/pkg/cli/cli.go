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

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/backup"
	"github.com/SSStq1314/hengdesk/server/internal/config"
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
	root.AddCommand(serverCmd(), workerCmd(), migrateCmd())
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
		var oidcSvc *auth.OIDC
		if cfg.OIDCIssuer != "" {
			oidcSvc = &auth.OIDC{R: rdb, Cfg: auth.OIDCConfig{Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID,
				ClientSecret: cfg.OIDCClientSecret, RedirectURL: cfg.OIDCRedirectURL, Scopes: cfg.OIDCScopes,
				LinkBy: cfg.OIDCLinkBy, AutoCreate: cfg.OIDCAutoCreate, OrgSlug: "default", DisplayName: cfg.OIDCDisplayName,
				EmailDomains: cfg.OIDCEmailDomains, DeptClaim: cfg.OIDCDeptClaim, DeptMatch: cfg.OIDCDeptMatch, DeptDefault: cfg.OIDCDeptDefault}}
			lg.Info("oidc enabled", "issuer", cfg.OIDCIssuer, "link_by", cfg.OIDCLinkBy, "auto_create", cfg.OIDCAutoCreate,
				"auto_create_effective", cfg.OIDCAutoCreate && len(cfg.OIDCEmailDomains) > 0, "dept_claim", cfg.OIDCDeptClaim, "enforce", cfg.OIDCEnforce)
		}
		api.Mount(e, api.Deps{DB: client, Audit: w, Log: lg, Auth: &auth.Service{OIDC: oidcSvc, EnforceSSO: cfg.OIDCEnforce, BreakGlass: cfg.OIDCBreakGlass,
			DB: client, Audit: w, Secure: cfg.CookieSecure,
			Sessions:  &session.Store{R: rdb, TTL: cfg.SessionTTL, IdleTimeout: cfg.SessionIdle},
			Lockout:   &session.Lockout{R: rdb, MaxFailures: cfg.LockoutMax, Duration: cfg.LockoutFor},
			IPLimit:   &session.RateLimiter{R: rdb, Limit: cfg.LoginIPLimit, Window: cfg.LoginIPWindow},
			Passwords: auth.PasswordPolicy{MinLen: cfg.PasswordMinLen},
		}, System: api.SystemConfig{BackupDir: cfg.BackupDir, BackupKeep: cfg.BackupKeep, DiskLowPercent: cfg.DiskLowPercent,
			PublicHTTPS: cfg.PublicHTTPS, TrustedProxies: cfg.TrustedProxies, TLSCertFile: cfg.TLSCertFile}})
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
		srv := asynq.NewServer(queue.RedisOpt(cfg.RedisAddr), asynq.Config{Concurrency: 10, Logger: nil})
		ctx, stop := signalCtx()
		defer stop()
		if err := srv.Start(queue.Mux(lg)); err != nil {
			return err
		}
		lg.Info("worker started", "redis", cfg.RedisAddr)
		<-ctx.Done()
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
