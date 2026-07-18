package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

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
	app, cleanup, err := bootstrap.InitializeWorker(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()
	if err := database.CheckMigrations(ctx, app.DB); err != nil {
		app.Logger.Fatal("数据库迁移状态无效，请先运行 tyrs-hand-admin migrate", zap.Error(err))
	}
	if err := app.Runner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		app.Logger.Fatal("Worker 退出", zap.Error(err))
	}
}
