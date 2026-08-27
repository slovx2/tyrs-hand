package worker

import (
	"context"
	"errors"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

// HostDesktopController 将 Desktop 投影语义接到唯一宿主 App Server Hub。
type HostDesktopController struct {
	*desktopController
}

func NewHostDesktopController(processor *Processor,
	manifest workerprotocol.WorkspaceManifest,
) *HostDesktopController {
	workspace := &workspaceCodex{
		manifest: manifest,
		runtime: workspaceRuntime{
			WorkspaceID: manifest.WorkspaceID,
		},
		generation: time.Now().UnixNano(),
		processor:  processor,
	}
	return &HostDesktopController{desktopController: &desktopController{
		processor: processor, workspace: workspace,
	}}
}

func (c *HostDesktopController) AttachRuntime(ctx context.Context,
	runtime *hostworker.Runtime,
) error {
	if c == nil || c.desktopController == nil || c.workspace == nil ||
		c.processor == nil || c.processor.workspaces == nil || runtime == nil {
		return errors.New("宿主 Desktop Controller 配置不完整")
	}
	workspace := c.workspace
	workspace.mu.Lock()
	if workspace.hostRuntime != nil {
		workspace.mu.Unlock()
		return errors.New("宿主 Desktop Controller 已绑定 Runtime")
	}
	workspace.hostRuntime = runtime
	workspace.mu.Unlock()
	if !c.bindRuntimeGeneration(ctx) {
		return errors.New("宿主 Codex Runtime 尚未就绪")
	}

	registry := c.processor.workspaces
	registry.mu.Lock()
	registry.entries[workspace.runtime.WorkspaceID] = workspace
	registry.mu.Unlock()
	go c.monitorRuntimeGenerations(ctx)
	go c.reconcileControlState(ctx)
	go c.runSessionTitleLoop(ctx)
	return nil
}

func (c *HostDesktopController) bindRuntimeGeneration(ctx context.Context) bool {
	workspace := c.workspace
	runtime := workspace.hostRuntime
	client, generation := runtime.ClientSnapshot()
	if client == nil || generation == 0 {
		return false
	}
	workspace.mu.Lock()
	if workspace.client == client && workspace.generation == generation {
		workspace.mu.Unlock()
		return true
	}
	previous := workspace.metadataEvents
	subscription := client.Subscribe(codex.ThreadFilter{})
	workspace.client = client
	workspace.generation = generation
	workspace.metadataEvents = subscription
	workspace.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	go workspace.observeMetadata(ctx, subscription)
	go workspace.reconcileThreadLifecycles(ctx, client)
	return true
}

func (c *HostDesktopController) monitorRuntimeGenerations(ctx context.Context) {
	changes := c.workspace.hostRuntime.GenerationChanges()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.bindRuntimeGeneration(ctx)
		case _, ok := <-changes:
			if !ok {
				return
			}
			if !c.bindRuntimeGeneration(ctx) && ctx.Err() == nil {
				c.processor.logger.Warn("Codex App Server 恢复后重新绑定 Desktop Controller 失败")
			}
		}
	}
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
	manifest, err := c.processor.client.Workspace(ctx)
	if err != nil {
		return err
	}
	if manifest == nil {
		return errors.New("宿主 Worker 尚未绑定 Workspace")
	}
	workspace := c.workspace
	workspace.mu.Lock()
	currentID := workspace.runtime.WorkspaceID
	if manifest.WorkspaceID == currentID {
		workspace.manifest = *manifest
	}
	workspace.mu.Unlock()
	if manifest.WorkspaceID != currentID {
		return errors.New("worker Workspace 绑定已变化，请重启宿主 Worker")
	}
	projects, scanErr := hostworker.ScanProjects(ctx, c.processor.cfg.WorkerWorkspaceRoot, c.processor.hostRuntime.CodexHome())
	request := workerprotocol.WorkspaceProjectSnapshotRequest{
		WorkspaceID: currentID, Projects: projects,
	}
	if scanErr != nil {
		request.Error = scanErr.Error()
		request.Projects = nil
	}
	if err := c.processor.client.WorkspaceProjectSnapshot(ctx, request); err != nil {
		return err
	}
	return scanErr
}

var _ appserverhub.Controller = (*HostDesktopController)(nil)
var _ appserverhub.ArchiveGate = (*HostDesktopController)(nil)
var _ appserverhub.EphemeralThreadConfigurator = (*HostDesktopController)(nil)
