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

type RemoteWorkerApp struct {
	Runner  *worker.RemoteRunner
	Runtime *hostworker.Runtime
	SSH     *hostworker.SSHServer
	Logger  *zap.Logger
}

func InitializeRemoteWorker(ctx context.Context, cfg config.Config) (*RemoteWorkerApp, func(), error) {
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
	processor := worker.NewRemoteProcessor(ctx, cfg, client, provideWorkspace(cfg), catalog,
		nil, nil, logger)
	runner, err := worker.NewRemoteRunner(cfg, client, processor, logger)
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, err
	}
	if err := runner.Authenticate(ctx); err != nil {
		cleanupFailure(nil)
		return nil, nil, fmt.Errorf("认证宿主 Worker: %w", err)
	}
	manifests, err := client.DevelopmentEnvironments(ctx)
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, fmt.Errorf("读取宿主 Worker 逻辑环境: %w", err)
	}
	if len(manifests) > 1 {
		cleanupFailure(nil)
		return nil, nil, fmt.Errorf("宿主 Worker 只能绑定一个逻辑环境，Control 返回了 %d 个",
			len(manifests))
	}
	var desktopController *worker.HostDesktopController
	runtimeOptions := hostworker.RuntimeOptions{
		CodexBin: cfg.CodexBin, CodexHome: cfg.WorkerCodexHome, Home: cfg.WorkerHome,
		WorkspaceRoot: cfg.WorkerWorkspaceRoot, StateDir: cfg.WorkerDataRoot, Logger: logger,
		SSHAuthSock: filepath.Join(cfg.SSHAgentDir, "current.sock"),
	}
	if len(manifests) == 1 {
		desktopController = worker.NewHostDesktopController(processor, manifests[0])
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
	if cfg.BrowserAgentRelayAddress != "" {
		sshOptions.BrowserProxy = hostworker.TCPProxy(cfg.BrowserAgentRelayAddress)
	}
	sshServer, err := hostworker.StartSSHServer(ctx, sshOptions)
	if err != nil {
		cleanupFailure(runtime)
		return nil, nil, err
	}
	app := &RemoteWorkerApp{Runner: runner, Runtime: runtime, SSH: sshServer, Logger: logger}
	return app, func() {
		_ = sshServer.Close()
		_ = runtime.Close()
		cleanupLogger()
	}, nil
}
