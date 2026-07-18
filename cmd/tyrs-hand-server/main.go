package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/slovx2/tyrs-hand/internal/bootstrap"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if len(cfg.MasterKey) != 32 {
		log.Fatal("必须配置 TYRS_HAND_MASTER_KEY")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, cleanup, err := bootstrap.InitializeServer(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()
	if err := database.CheckMigrations(ctx, app.DB); err != nil {
		app.Logger.Fatal("数据库迁移状态无效，请先运行 tyrs-hand-admin migrate", zap.Error(err))
	}
	servers := []struct {
		name   string
		server *http.Server
	}{{name: "http", server: newHTTPServer(cfg.HTTPAddr, app.API.Router())}}
	if cfg.SeparateWebhook {
		servers = []struct {
			name   string
			server *http.Server
		}{
			{name: "admin", server: newHTTPServer(cfg.HTTPAddr, app.API.AdminRouter())},
			{name: "webhook", server: newHTTPServer(cfg.WebhookHTTPAddr, app.API.WebhookRouter())},
		}
	}
	for _, instance := range servers {
		go func() {
			app.Logger.Info("tyrs-hand HTTP server started", zap.String("name", instance.name), zap.String("addr", instance.server.Addr))
			if err := instance.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				app.Logger.Fatal("HTTP Server 退出", zap.String("name", instance.name), zap.Error(err))
			}
		}()
	}
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, instance := range servers {
		if err := instance.server.Shutdown(shutdownCtx); err != nil {
			app.Logger.Error("HTTP Server 优雅退出失败", zap.String("name", instance.name), zap.Error(err))
		}
	}
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 0, IdleTimeout: 120 * time.Second,
	}
}
