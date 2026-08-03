package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/slovx2/tyrs-hand/internal/bootstrap"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"go.uber.org/zap"
)

func main() {
	cfg, err := config.LoadWorker()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) == 2 && os.Args[1] == "doctor" {
		checks, doctorErr := hostworker.Doctor(ctx, hostworker.RuntimeOptions{
			CodexBin: cfg.CodexBin, CodexHome: cfg.WorkerCodexHome, Home: cfg.WorkerHome,
			WorkspaceRoot: cfg.WorkerWorkspaceRoot, StateDir: cfg.WorkerDataRoot,
		}, cfg.WorkerShell, cfg.WorkerAuthorizedKeysFile)
		for _, check := range checks {
			fmt.Printf("%-18s %s (%s)\n", check.Name, check.Status, check.Path)
		}
		if doctorErr != nil {
			log.Fatal(doctorErr)
		}
		return
	}
	app, cleanup, initializeErr := bootstrap.InitializeRemoteWorker(ctx, cfg)
	if initializeErr != nil {
		log.Fatal(initializeErr)
	}
	defer cleanup()
	app.Logger.Info("宿主 Worker 已启动", zap.String("ssh", app.SSH.Addr().String()),
		zap.String("home", cfg.WorkerHome), zap.String("codex_home", cfg.WorkerCodexHome),
		zap.String("workspace_root", cfg.WorkerWorkspaceRoot))
	if runErr := app.Runner.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
		app.Logger.Fatal("宿主 Worker 退出", zap.Error(runErr))
	}
}
