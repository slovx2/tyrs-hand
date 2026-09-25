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
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"go.uber.org/zap"
)

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "runtime-entry" {
		if err := hostworker.RunEntryCommand(context.Background(), os.Args[2], os.Args[3:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	cfg, err := config.LoadWorker()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) == 2 && os.Args[1] == "doctor" {
		checks, doctorErr := hostworker.Doctor(ctx, hostworker.RuntimeOptions{Engine: runtimeidentity.Codex,
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
	app, cleanup, initializeErr := bootstrap.InitializeWorker(ctx, cfg)
	if initializeErr != nil {
		log.Fatal(initializeErr)
	}
	app.Logger.Info("宿主 Worker 已启动", zap.String("ssh", cfg.WorkerSSHListenAddr),
		zap.String("home", cfg.WorkerHome), zap.String("codex_home", cfg.WorkerCodexHome),
		zap.String("workspace_root", cfg.WorkerWorkspaceRoot),
		zap.Bool("control_sync", cfg.ControlSyncEnabled()))
	runErr := app.Run(ctx)
	cleanup()
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		log.Fatalf("宿主 Worker 退出: %v", runErr)
	}
}
