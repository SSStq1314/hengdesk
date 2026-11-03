// Command hengdesk 是衡台单二进制入口：server / worker / migrate。
package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/config"
	"github.com/SSStq1314/hengdesk/server/internal/httpserver"
	hlog "github.com/SSStq1314/hengdesk/server/internal/log"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

func main() {
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
		w := audit.EntWriter{C: client}
		api.Mount(e, api.Deps{DB: client, Audit: w, Log: lg, Auth: &auth.Service{
			DB: client, Audit: w, Secret: []byte(cfg.JWTSecret), TTL: cfg.SessionTTL, Secure: cfg.CookieSecure}})
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
		lg.Info("worker started (placeholder)")
		ctx, stop := signalCtx()
		defer stop()
		<-ctx.Done()
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
	return &cobra.Command{Use: "migrate", Short: "执行数据库迁移并初始化管理员", RunE: func(_ *cobra.Command, _ []string) error {
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
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := waitDB(ctx, db); err != nil {
			return err
		}
		if err := store.Migrate(ctx, client, db, true); err != nil {
			return err
		}
		created, err := store.Seed(ctx, client, store.SeedOptions{OrgName: "默认组织", OrgSlug: "default",
			AdminEmail: cfg.AdminEmail, AdminPassword: cfg.AdminPassword})
		if err != nil {
			return err
		}
		lg.Info("migrate done", "admin_email", cfg.AdminEmail, "admin_created", created)
		return nil
	}}
}
