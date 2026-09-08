package worker

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcatalog"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

// HostDesktopController 常驻宿主；Workspace 集成按绑定快照替换，不重启运行时。
type HostDesktopController struct {
	processor        *Processor
	mu               sync.Mutex
	runtime          *hostworker.Runtime
	integration      *desktopController
	active           map[string]*hostCallState
	metadataSequence atomic.Int64
	settingsSequence atomic.Int64
}

func NewHostDesktopController(processor *Processor, manifest *workerprotocol.WorkspaceManifest) *HostDesktopController {
	c := &HostDesktopController{processor: processor, active: make(map[string]*hostCallState)}
	c.setBinding(manifest)
	return c
}

func (c *HostDesktopController) snapshot() (*desktopController, *hostworker.Runtime) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.integration, c.runtime
}

// setBinding 创建不可变身份快照。已开始的 turn 继续引用原快照。
func (c *HostDesktopController) setBinding(manifest *workerprotocol.WorkspaceManifest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.integration
	if previous == nil && manifest == nil {
		return
	}
	if previous != nil && manifest != nil && reflect.DeepEqual(previous.workspace.manifest, *manifest) {
		return
	}
	if previous != nil {
		previous.workspace.mu.Lock()
		if previous.workspace.metadataEvents != nil {
			previous.workspace.metadataEvents.Close()
		}
		previous.workspace.mu.Unlock()
	}
	c.integration = nil
	if manifest != nil {
		// 不保留调用者可变的身份指针及 Forum 切片。
		copyManifest := *manifest
		if manifest.OwnerParticipant != nil {
			owner := *manifest.OwnerParticipant
			copyManifest.OwnerParticipant = &owner
		}
		copyManifest.Forums = slices.Clone(manifest.Forums)
		for index := range copyManifest.Forums {
			if id := copyManifest.Forums[index].ProjectID; id != nil {
				value := *id
				copyManifest.Forums[index].ProjectID = &value
			}
		}
		workspace := &workspaceCodex{manifest: copyManifest,
			runtime:   workspaceRuntime{WorkspaceID: manifest.WorkspaceID},
			processor: c.processor, hostRuntime: c.runtime, generation: time.Now().UnixNano(),
			metadataSequence: &c.metadataSequence, settingsSequence: &c.settingsSequence}
		integration := &desktopController{processor: c.processor, workspace: workspace}
		integration.controlValid = func() bool {
			current, _ := c.snapshot()
			return current == integration
		}
		workspace.controlValid = integration.controlValid
		workspace.metadataAllowed = func(threadID string) bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			turn := c.active[threadID]
			return c.integration == integration && (turn == nil || turn.controller == integration)
		}
		c.integration = integration
	}
	registry := c.processor.workspaces
	if registry != nil {
		registry.mu.Lock()
		if previous != nil {
			delete(registry.entries, previous.workspace.runtime.WorkspaceID)
		}
		if c.integration != nil {
			registry.entries[manifest.WorkspaceID] = c.integration.workspace
		}
		registry.mu.Unlock()
	}
	if c.runtime != nil && c.integration != nil {
		c.bindClientLocked(c.integration.workspace, c.runtime.Client(), c.runtime.Generation())
	}
}

func (c *HostDesktopController) AttachRuntime(ctx context.Context, runtime *hostworker.Runtime) error {
	if c == nil || c.processor == nil || c.processor.workspaces == nil || runtime == nil {
		return errors.New("宿主 Desktop Controller 配置不完整")
	}
	c.mu.Lock()
	if c.runtime != nil {
		c.mu.Unlock()
		return errors.New("宿主 Desktop Controller 已绑定 Runtime")
	}
	c.runtime = runtime
	if c.integration != nil {
		c.integration.workspace.hostRuntime = runtime
		c.bindClientLocked(c.integration.workspace, runtime.Client(), runtime.Generation())
	}
	c.mu.Unlock()
	go c.reconcileControlState(ctx)
	go c.runSessionTitleLoop(ctx)
	return nil
}

func (c *HostDesktopController) bindClientLocked(workspace *workspaceCodex, client *appserverhub.Client, generation int64) {
	if client == nil {
		return
	}
	subscription := client.Subscribe(codex.ThreadFilter{})
	workspace.mu.Lock()
	previous := workspace.metadataEvents
	workspace.client, workspace.generation, workspace.metadataEvents = client, generation, subscription
	workspace.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	// 仅订阅新活动，不枚举或导入绑定前的历史 Thread。
	go workspace.observeMetadata(c.processor.workspaces.ctx, subscription)
}

func (c *HostDesktopController) RebindRuntime(_ context.Context, client *appserverhub.Client, generation int64) error {
	if client == nil || generation == 0 {
		return errors.New("宿主 Codex Runtime Client 不可用")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.integration != nil {
		c.bindClientLocked(c.integration.workspace, client, generation)
	}
	for _, turn := range c.active {
		if turn.controller != nil && turn.controller != c.integration {
			turn.controller.workspace.mu.Lock()
			turn.controller.workspace.client, turn.controller.workspace.generation = client, generation
			turn.controller.workspace.mu.Unlock()
		}
	}
	return nil
}

func (c *HostDesktopController) reconcileControlState(ctx context.Context) {
	interval := max(c.processor.cfg.HeartbeatInterval, 15*time.Second)
	reconcile := func() {
		if err := c.syncHostEnvironment(ctx); err != nil && ctx.Err() == nil {
			c.processor.logger.Warn("同步宿主绑定失败，保留最近确认状态", zap.Error(err))
		}
		integration, runtime := c.snapshot()
		if integration != nil {
			if err := errors.Join(c.processor.applyPendingThreadNames(ctx), c.processor.applyPendingThreadLifecycles(ctx)); err != nil && ctx.Err() == nil {
				c.processor.logger.Warn("同步宿主 Desktop Thread 状态失败", zap.Error(err))
			}
		}
		if runtime != nil && runtime.Client() != nil {
			fetchCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
			catalog, err := codexcatalog.Fetch(fetchCtx, runtime.Client())
			cancel()
			if err == nil {
				c.processor.SetModelCatalog(catalog)
			} else if ctx.Err() == nil {
				c.processor.logger.Warn("获取宿主模型目录失败，将后台重试", zap.Error(err))
			}
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
	requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
	defer cancel()
	manifest, err := c.processor.client.Workspace(requestCtx)
	if err != nil {
		return err
	}
	c.setBinding(manifest)
	return SaveWorkspaceManifest(c.processor.cfg.WorkerDataRoot, manifest)
}

var _ appserverhub.Controller = (*HostDesktopController)(nil)
var _ appserverhub.ArchiveGate = (*HostDesktopController)(nil)
var _ appserverhub.EphemeralThreadConfigurator = (*HostDesktopController)(nil)
var _ hostworker.RuntimeRebinder = (*HostDesktopController)(nil)
