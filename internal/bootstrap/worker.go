package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcatalog"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/slovx2/tyrs-hand/internal/worker"
	"github.com/slovx2/tyrs-hand/internal/workerconfig"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type WorkerApp struct {
	Runner   *worker.Runner
	Runtimes *hostworker.RuntimeRegistry
	Logger   *zap.Logger
}

func (a *WorkerApp) Run(ctx context.Context) error {
	return superviseWorker(ctx, a.Runner.Run)
}

func superviseWorker(ctx context.Context, run func(context.Context) error) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- run(runCtx) }()
	select {
	case err := <-runnerDone:
		return err
	case <-ctx.Done():
		cancel()
		<-runnerDone
		return ctx.Err()
	}
}

func InitializeWorker(ctx context.Context, cfg config.Config) (*WorkerApp, func(), error) {
	ctx, cancelWorker := context.WithCancel(ctx)
	logger, cleanupLogger, err := provideLogger(cfg)
	if err != nil {
		cancelWorker()
		return nil, nil, err
	}
	dataLock, err := worker.AcquireDataLock(cfg.WorkerDataRoot)
	if err != nil {
		cancelWorker()
		cleanupLogger()
		return nil, nil, err
	}
	if err := platformsettings.InstallBuiltinSkills(cfg.WorkerCodexHome); err != nil {
		cancelWorker()
		_ = dataLock.Close()
		cleanupLogger()
		return nil, nil, fmt.Errorf("安装宿主 Codex Skill 失败: %w", err)
	}
	cleanupFailure := func(runtime *hostworker.Runtime) {
		cancelWorker()
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
	if err := worker.MigrateCodexJournalRuntime(cfg.WorkerDataRoot); err != nil {
		cleanupFailure(nil)
		return nil, nil, fmt.Errorf("迁移 Codex Journal 引擎: %w", err)
	}
	processor := worker.NewProcessor(ctx, cfg, client, provideWorkspace(cfg), catalog, logger)
	runner, err := worker.NewRunner(cfg, client, processor, logger)
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, err
	}
	if cfg.ControlSyncEnabled() {
		if err := runner.Authenticate(ctx); err != nil {
			cleanupFailure(nil)
			return nil, nil, fmt.Errorf("认证宿主 Worker: %w", err)
		}
	} else {
		if err := runner.InitializeOfflineIdentity(); err != nil {
			cleanupFailure(nil)
			return nil, nil, err
		}
		logger.Info("已关闭与 Control 的通信，跳过注册、心跳、任务领取和状态上报")
	}
	credentialBytes, err := os.ReadFile(cfg.WorkerCredentialFile)
	var configService *workerconfig.Service
	var credential string
	if err == nil {
		credential = strings.TrimSpace(string(credentialBytes))
		configService = workerconfig.NewServiceWithStateDirAndEnv(cfg.WorkerCodexHome, cfg.CodexBin, cfg.WorkerDataRoot, cfg.WorkerGlobalEnvFile)
	}
	var manifest *workerprotocol.WorkspaceManifest
	if cfg.ControlSyncEnabled() {
		manifest, err = client.Workspace(ctx)
		if err != nil {
			controlErr := err
			manifest, err = worker.LoadCachedWorkspaceManifest(cfg.WorkerDataRoot)
			if err != nil {
				manifest = nil
				logger.Warn("Control 不可用且没有有效 Workspace 快照，启动宿主基础能力", zap.Error(err))
			} else {
				logger.Warn("Control 暂不可用，使用本地 Workspace 快照启动", zap.Error(controlErr))
			}
		} else {
			if cacheErr := worker.SaveWorkspaceManifest(cfg.WorkerDataRoot, manifest); cacheErr != nil {
				cleanupFailure(nil)
				return nil, nil, fmt.Errorf("保存宿主 Workspace 快照: %w", cacheErr)
			}
		}
	} else {
		manifest, err = worker.LoadCachedWorkspaceManifest(cfg.WorkerDataRoot)
		if err != nil {
			manifest = nil
			logger.Warn("没有有效 Workspace 快照，以本地宿主能力启动", zap.Error(err))
		}
	}
	desktopController := worker.NewHostDesktopController(processor, manifest)
	var claudeProcessor *worker.Processor
	var claudeController *worker.HostDesktopController
	if cfg.WorkerClaudeEnabled {
		if err := platformsettings.InstallBuiltinSkills(cfg.ClaudeConfigDir()); err != nil {
			cleanupFailure(nil)
			return nil, nil, fmt.Errorf("安装 Claude Skill 失败: %w", err)
		}
		claudeConfig := cfg
		claudeConfig.WorkerDataRoot = cfg.ClaudeStateDir()
		claudeConfig.WorkerHome = cfg.ClaudeHome()
		claudeConfig.WorkerCodexHome = cfg.ClaudeAdapterHome()
		// Control 尚未完成引擎作用域迁移，禁止 Claude 复用 Codex 的事务与投影。
		// 此阶段只开放明确隔离的 SSH 会话，发布门禁继续保持未完成。
		claudeConfig.WorkerDisableControlSync = true
		claudeProcessor = worker.NewProcessor(ctx, claudeConfig, nil, provideWorkspace(cfg), catalog, logger)
		claudeProcessor.ShareTurnBudget(processor)
		claudeController = worker.NewHostDesktopController(claudeProcessor, nil)
	}
	runtimeOptions := hostworker.RuntimeOptions{Engine: runtimeidentity.Codex,
		WorkerID: runner.WorkerID(),
		CodexBin: cfg.CodexBin, CodexHome: cfg.WorkerCodexHome, Home: cfg.WorkerHome,
		WorkspaceRoot: cfg.WorkerWorkspaceRoot, StateDir: cfg.WorkerDataRoot, Logger: logger,
		EnvFile:     cfg.WorkerGlobalEnvFile,
		SSHAuthSock: filepath.Join(cfg.SSHAgentDir, "current.sock"),
	}
	runtimeOptions.Controller = desktopController
	scopeID := uuid.Nil
	if cfg.BrowserMCPURL != "" {
		scopeID, err = worker.LoadBrowserScope(cfg.WorkerDataRoot)
		if err != nil {
			cleanupFailure(nil)
			return nil, nil, err
		}
	}
	if cfg.BrowserMCPURL != "" && scopeID != uuid.Nil {
		runtimeOptions.BrowserServiceSocket = filepath.Join(cfg.BrowserServicesRoot,
			scopeID.String(), "proxy.sock")
	}
	browserTokens, err := worker.DeriveBrowserAppServerTokens(cfg, scopeID)
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, err
	}
	runtimeOptions.BrowserWorkerToken = browserTokens.Worker
	runtimeOptions.BrowserDesktopToken = browserTokens.Desktop
	clients, err := hostworker.LoadAuthorizedClients(cfg.WorkerAuthorizedKeysFile)
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, err
	}
	sshOptions := hostworker.SSHOptions{
		ListenAddr: cfg.WorkerSSHListenAddr, HostKeyFile: cfg.WorkerSSHHostKeyFile,
		Home: cfg.WorkerHome, CodexHome: cfg.WorkerCodexHome, Shell: cfg.WorkerShell,
		AuthorizedClients: clients, Logger: logger,
	}
	if cfg.BrowserAgentAddress != "" && browserTokens.Desktop != "" {
		sshOptions.BrowserProxy = hostworker.BrowserAgentProxy(cfg.BrowserAgentAddress, browserTokens.Desktop)
	}
	registry, err := hostworker.StartRuntimeRegistry(ctx, workerRuntimeEntries(cfg, runtimeOptions, sshOptions, claudeController))
	if err != nil {
		cleanupFailure(nil)
		return nil, nil, err
	}
	entry, err := registry.Entry(runtimeidentity.Codex)
	if err != nil {
		_ = registry.Close()
		cleanupFailure(nil)
		return nil, nil, err
	}
	runtime := entry.Runtime
	if configService != nil && cfg.ControlSyncEnabled() {
		configService.SetWorkspaceRoot(cfg.WorkerWorkspaceRoot)
		configService.SetRestart(func() error { return registry.Restart(runtimeidentity.Codex) })
		claudeConfig := workerconfig.NewClaudeService(cfg.ClaudeConfigDir())
		claudeConfig.SetRestart(func() error { return registry.Restart(runtimeidentity.Claude) })
		go runControlChannel(ctx, cfg, credential, configService, claudeConfig, runner, logger)
	}
	var modelCatalog json.RawMessage
	if client := runtime.Client(); client != nil {
		catalogCtx, cancel := context.WithTimeout(ctx, cfg.ControlTimeout)
		modelCatalog, err = codexcatalog.Fetch(catalogCtx, client)
		cancel()
		if err != nil {
			logger.Warn("宿主模型目录暂不可用，将后台重试", zap.Error(err))
		}
	}
	processor.UseHostRuntime(runtime, scopeID, modelCatalog)
	if err := desktopController.AttachRuntime(ctx, runtime); err != nil {
		_ = registry.Close()
		cleanupFailure(nil)
		return nil, nil, err
	}
	if claudeController != nil {
		claudeEntry, _ := registry.Entry(runtimeidentity.Claude)
		claudeProcessor.UseHostRuntime(claudeEntry.Runtime, scopeID, nil)
		if err := claudeController.AttachRuntime(ctx, claudeEntry.Runtime); err != nil {
			_ = registry.Close()
			cleanupFailure(nil)
			return nil, nil, err
		}
	}
	runner.SetSSHHostKeyFingerprint(entry.SSH.HostKeyFingerprint())
	runner.SetRuntimeReports(func() []workerprotocol.RuntimeReport {
		return runtimeReports(registry, map[runtimeidentity.Engine]*worker.Processor{
			runtimeidentity.Codex: processor, runtimeidentity.Claude: claudeProcessor,
		})
	})
	registry.WatchAuthorizedClients(cfg.WorkerAuthorizedKeysFile, logger)
	app := &WorkerApp{Runner: runner, Runtimes: registry, Logger: logger}
	return app, func() {
		cancelWorker()
		_ = registry.Close()
		_ = dataLock.Close()
		cleanupLogger()
	}, nil
}

const (
	controlChannelMinBackoff   = 3 * time.Second
	controlChannelMaxBackoff   = 30 * time.Second
	controlChannelResetBackoff = 30 * time.Second
)

// runControlChannel 维护 Worker 到 Control 的 WebSocket 控制通道。
// 通道承载 hello 能力协商、配置 RPC 与唤醒推送；断开后指数退避重连，
// 并在每次连接建立后触发一次全量同步，覆盖断连期间漏掉的唤醒。
func runControlChannel(ctx context.Context, cfg config.Config, credential string,
	service *workerconfig.Service, claude *workerconfig.ClaudeService, runner *worker.Runner, logger *zap.Logger,
) {
	backoff := controlChannelMinBackoff
	for ctx.Err() == nil {
		runner.SetControlChannelState(false, false)
		started := time.Now()
		err := workerconfig.RunChannel(ctx, workerconfig.ChannelOptions{
			ControlURL: cfg.WorkerControlURL, Credential: credential, Service: service, Claude: claude,
			ProtocolVersion: cfg.WorkerProtocolVersion,
			Notify:          runner.NotifyControlWake,
			Ready: func(wake bool) {
				runner.SetControlChannelState(true, wake)
				logger.Info("Worker 控制通道已就绪", zap.Bool("wake", wake))
				runner.NotifyControlWake(workerprotocol.AllWakeKinds())
			},
		})
		runner.SetControlChannelState(false, false)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.Warn("Worker 控制通道断开", zap.Error(err))
		}
		if time.Since(started) >= controlChannelResetBackoff {
			backoff = controlChannelMinBackoff
		}
		if !waitControlReconnect(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, controlChannelMaxBackoff)
	}
}

// waitControlReconnect 在退避时间上加入抖动，避免多台 Worker 同时重连。
func waitControlReconnect(ctx context.Context, backoff time.Duration) bool {
	jitter := time.Duration(rand.Int63n(int64(backoff)/4 + 1))
	timer := time.NewTimer(backoff + jitter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
