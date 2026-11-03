// Command hengdesk 是衡台单二进制入口：server / worker / migrate。
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/SSStq1314/hengdesk/server/internal/config"
	"github.com/SSStq1314/hengdesk/server/internal/httpserver"
	hlog "github.com/SSStq1314/hengdesk/server/internal/log"
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
		e := httpserver.New(lg)
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

func migrateCmd() *cobra.Command {
	return &cobra.Command{Use: "migrate", Short: "执行数据库迁移并初始化管理员", RunE: func(_ *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		hlog.New(cfg.LogLevel).Info("migrate (placeholder)")
		return nil
	}}
}
