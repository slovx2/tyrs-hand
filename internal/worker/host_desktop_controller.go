package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexrelay"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

// HostDesktopController 将原有 Desktop 投影语义接到唯一宿主 App Server。
// Environment 只作为 Control 中的项目、Forum 和参与者绑定，不代表容器运行时。
type HostDesktopController struct {
	*desktopRelayController
}

func NewHostDesktopController(processor *RemoteProcessor,
	manifest workerprotocol.EnvironmentManifest,
) *HostDesktopController {
	environment := &environmentCodex{
		manifest: manifest,
		runtime: devcontainer.Runtime{
			EnvironmentID: manifest.EnvironmentID,
		},
		generation:          time.Now().UnixNano(),
		processor:           processor,
		toolHandlers:        make(map[string]toolBinding),
		interactiveHandlers: make(map[string]interactiveBinding),
	}
	return &HostDesktopController{desktopRelayController: &desktopRelayController{
		processor: processor, environment: environment,
	}}
}

func (c *HostDesktopController) AttachRuntime(ctx context.Context,
	runtime *hostworker.Runtime,
) error {
	if c == nil || c.desktopRelayController == nil || c.environment == nil ||
		c.processor == nil || c.processor.environments == nil || runtime == nil {
		return errors.New("宿主 Desktop Controller 配置不完整")
	}
	environment := c.environment
	environment.mu.Lock()
	if environment.hostRuntime != nil {
		environment.mu.Unlock()
		return errors.New("宿主 Desktop Controller 已绑定 Runtime")
	}
	environment.hostRuntime = runtime
	environment.client = runtime.Client()
	environment.generation = runtime.Generation()
	environment.mu.Unlock()

	registry := c.processor.environments
	registry.mu.Lock()
	registry.entries[environment.runtime.EnvironmentID] = environment
	registry.mu.Unlock()
	environment.metadataEvents = environment.client.Subscribe(codex.ThreadFilter{})
	go environment.observeMetadata(ctx)
	go environment.reconcileThreadLifecycles(ctx)
	go c.reconcileControlState(ctx)
	return nil
}

func (c *HostDesktopController) reconcileControlState(ctx context.Context) {
	interval := c.processor.cfg.HeartbeatInterval
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}
	reconcile := func() {
		err := errors.Join(c.syncHostEnvironment(ctx),
			c.processor.applyPendingThreadNames(ctx),
			c.processor.applyPendingThreadLifecycles(ctx))
		if err != nil && ctx.Err() == nil {
			c.processor.logger.Warn("同步宿主 Desktop Thread 状态失败", zap.Error(err))
		}
	}
	reconcile()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

func (c *HostDesktopController) syncHostEnvironment(ctx context.Context) error {
	manifests, err := c.processor.client.DevelopmentEnvironments(ctx)
	if err != nil {
		return err
	}
	if len(manifests) != 1 {
		return fmt.Errorf("宿主 Worker 必须绑定一个逻辑环境，Control 返回了 %d 个", len(manifests))
	}
	environment := c.environment
	environment.mu.Lock()
	currentID := environment.runtime.EnvironmentID
	if manifests[0].EnvironmentID == currentID {
		environment.manifest = manifests[0]
	}
	environment.mu.Unlock()
	if manifests[0].EnvironmentID != currentID {
		return errors.New("worker 逻辑环境绑定已变化，请重启宿主 Worker")
	}
	projects, scanErr := hostworker.ScanProjects(ctx, c.processor.cfg.WorkerWorkspaceRoot)
	request := workerprotocol.DevelopmentProjectSnapshotRequest{
		EnvironmentID: currentID, Projects: projects,
	}
	if scanErr != nil {
		request.Error = scanErr.Error()
		request.Projects = nil
	}
	if err := c.processor.client.DevelopmentProjectSnapshot(ctx, request); err != nil {
		return err
	}
	state := workerprotocol.EnvironmentDaemonState{
		EnvironmentID: currentID, Status: "running", AppServerStatus: "running",
		SSHStatus: "running", HubStatus: "running",
	}
	if err := c.processor.client.EnvironmentDaemonState(ctx, state); err != nil {
		return err
	}
	return scanErr
}

var _ codexrelay.Controller = (*HostDesktopController)(nil)
var _ codexrelay.ArchiveGate = (*HostDesktopController)(nil)
var _ codexrelay.EphemeralThreadConfigurator = (*HostDesktopController)(nil)
