package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcatalog"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
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

func (a *WorkerApp) Run(ctx context.Context) error {
	return superviseWorker(ctx, a.Runner.Run, a.Runtime.Done, a.Runtime.Err)
}

func superviseWorker(ctx context.Context, run func(context.Context) error,
	runtimeDone func() <-chan struct{}, runtimeErr func() error,
) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- run(runCtx) }()
	select {
	case err := <-runnerDone:
		return err
	case <-runtimeDone():
		cancel()
		<-runnerDone
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := runtimeErr(); err != nil {
			return fmt.Errorf("宿主 Codex App Server 异常退出: %w", err)
		}
		return fmt.Errorf("宿主 Codex App Server 异常退出")
	case <-ctx.Done():
		cancel()
		<-runnerDone
		return ctx.Err()
	}
}

func InitializeWorker(ctx context.Context, cfg config.Config) (*WorkerApp, func(), error) {
	logger, cleanupLogger, err := provideLogger(cfg)
	if err != nil {
		return nil, nil, err
	}
	dataLock, err := worker.AcquireDataLock(cfg.WorkerDataRoot)
	if err != nil {
		cleanupLogger()
		return nil, nil, err
	}
	if err := platformsettings.InstallBuiltinSkills(cfg.WorkerCodexHome); err != nil {
		_ = dataLock.Close()
		cleanupLogger()
		return nil, nil, fmt.Errorf("安装宿主 Codex Skill 失败: %w", err)
	}
	cleanupFailure := func(runtime *hostworker.Runtime) {
		if runtime != nil {
			_ = runtime.Close()
		}
		_ = dataLock.Close()
		cleanupLogger()
	}
	catalog, err := provideCatalog()
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, err
	}
	client := workerprotocol.NewClient(cfg.WorkerControlURL, "", cfg.ControlTimeout)
	processor := worker.NewProcessor(cfg, client, provideWorkspace(cfg), catalog, logger)
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
	if cfg.BrowserMCPURL != "" {
		encodedTool, marshalErr := json.Marshal(worker.BrowserToolSpec())
		if marshalErr != nil {
			cleanupFailure(nil)
			return nil, nil, marshalErr
		}
		runtimeOptions.BrowserMCPURL = cfg.BrowserMCPURL
		runtimeOptions.BrowserDynamicTool = encodedTool
	}
	if manifest != nil {
		desktopController = worker.NewHostDesktopController(processor, *manifest)
		if manifest.OwnerParticipant != nil {
			runtimeOptions.OwnerParticipant = &participantidentity.Participant{
				ID:          manifest.OwnerParticipant.ParticipantID,
				DisplayName: manifest.OwnerParticipant.DisplayName,
			}
		}
	}
	workspaceID := uuid.Nil
	if manifest != nil {
		workspaceID = manifest.WorkspaceID
	}
	if cfg.BrowserMCPURL != "" && workspaceID != uuid.Nil {
		runtimeOptions.BrowserServiceSocket = filepath.Join(cfg.BrowserServicesRoot,
			workspaceID.String(), "proxy.sock")
	}
	browserTokens, err := worker.DeriveBrowserAppServerTokens(cfg, workspaceID)
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, err
	}
	runtimeOptions.BrowserWorkerToken = browserTokens.Worker
	runtimeOptions.BrowserDesktopToken = browserTokens.Desktop
	runtime, err := hostworker.StartRuntime(ctx, runtimeOptions)
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, err
	}
	if desktopController != nil {
		if err := desktopController.Start(ctx); err != nil {
			cleanupFailure(runtime)
			return nil, nil, err
		}
	}
	var modelCatalog json.RawMessage
	if manifest != nil {
		catalogCtx, cancel := context.WithTimeout(ctx, cfg.ControlTimeout)
		modelCatalog, err = codexcatalog.Fetch(catalogCtx, runtime.Client())
		cancel()
		if err != nil {
			cleanupFailure(runtime)
			return nil, nil, fmt.Errorf("读取宿主 Codex 模型目录: %w", err)
		}
	}
	processor.UseHostRuntime(runtime, workspaceID, modelCatalog)
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
	if cfg.BrowserAgentAddress != "" && browserTokens.Desktop != "" {
		sshOptions.BrowserProxy = hostworker.BrowserAgentProxy(cfg.BrowserAgentAddress,
			browserTokens.Desktop)
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
		_ = dataLock.Close()
		cleanupLogger()
	}, nil
}
