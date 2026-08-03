package bootstrap

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/worker"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type WorkerApp struct {
	Runner  *worker.Runner
	Runtime *hostworker.Runtime
	SSH     *hostworker.SSHServer
	Logger  *zap.Logger
}

func InitializeWorker(ctx context.Context, cfg config.Config) (*WorkerApp, func(), error) {
	logger, cleanupLogger, err := provideLogger(cfg)
	if err != nil {
		return nil, nil, err
	}
	cleanupFailure := func(runtime *hostworker.Runtime) {
		if runtime != nil {
			_ = runtime.Close()
		}
		cleanupLogger()
	}
	catalog, err := provideCatalog()
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, err
	}
	client := workerprotocol.NewClient(cfg.WorkerControlURL, "", cfg.ControlTimeout)
	processor := worker.NewProcessor(ctx, cfg, client, provideWorkspace(cfg), catalog, logger)
	runner, err := worker.NewRunner(cfg, client, processor, logger)
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, err
	}
	if err := runner.Authenticate(ctx); err != nil {
		cleanupFailure(nil)
		return nil, nil, fmt.Errorf("认证宿主 Worker: %w", err)
	}
	manifest, err := client.Workspace(ctx)
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, fmt.Errorf("读取宿主 Worker 逻辑环境: %w", err)
	}
	var desktopController *worker.HostDesktopController
	runtimeOptions := hostworker.RuntimeOptions{
		CodexBin: cfg.CodexBin, CodexHome: cfg.WorkerCodexHome, Home: cfg.WorkerHome,
		WorkspaceRoot: cfg.WorkerWorkspaceRoot, StateDir: cfg.WorkerDataRoot, Logger: logger,
		SSHAuthSock: filepath.Join(cfg.SSHAgentDir, "current.sock"),
	}
	if manifest != nil {
		desktopController = worker.NewHostDesktopController(processor, *manifest)
		runtimeOptions.Controller = desktopController
	}
	runtime, err := hostworker.StartRuntime(ctx, runtimeOptions)
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, err
	}
	if desktopController != nil {
		if err := desktopController.AttachRuntime(ctx, runtime); err != nil {
			cleanupFailure(runtime)
			return nil, nil, err
		}
	}
	processor.UseHostRuntime(runtime)
	clients, err := hostworker.LoadAuthorizedClients(cfg.WorkerAuthorizedKeysFile)
	if err != nil {
		cleanupFailure(runtime)
		return nil, nil, err
	}
	sshOptions := hostworker.SSHOptions{
		ListenAddr: cfg.WorkerSSHListenAddr, HostKeyFile: cfg.WorkerSSHHostKeyFile,
		Home: cfg.WorkerHome, CodexHome: cfg.WorkerCodexHome, Shell: cfg.WorkerShell,
		AuthorizedClients: clients, Runtime: runtime, Logger: logger,
	}
	if cfg.BrowserAgentAddress != "" {
		sshOptions.BrowserProxy = hostworker.TCPProxy(cfg.BrowserAgentAddress)
	}
	sshServer, err := hostworker.StartSSHServer(ctx, sshOptions)
	if err != nil {
		cleanupFailure(runtime)
		return nil, nil, err
	}
	app := &WorkerApp{Runner: runner, Runtime: runtime, SSH: sshServer, Logger: logger}
	return app, func() {
		_ = sshServer.Close()
		_ = runtime.Close()
		cleanupLogger()
	}, nil
}
