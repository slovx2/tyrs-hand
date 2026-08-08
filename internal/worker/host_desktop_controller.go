package worker

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

// HostDesktopController 使用独立的官方连接观察宿主 App Server。
// Desktop 与移动端连接直接接入 App Server，不经过此观察器。
type HostDesktopController struct {
	processor   *Processor
	workspaceID uuid.UUID
}

func NewHostDesktopController(processor *Processor,
	manifest workerprotocol.WorkspaceManifest,
) *HostDesktopController {
	return &HostDesktopController{processor: processor, workspaceID: manifest.WorkspaceID}
}

func (c *HostDesktopController) Start(ctx context.Context) error {
	if c == nil || c.processor == nil || c.workspaceID == uuid.Nil {
		return errors.New("宿主 Workspace 同步器配置不完整")
	}
	go c.reconcileControlState(ctx)
	return nil
}

func (c *HostDesktopController) reconcileControlState(ctx context.Context) {
	interval := c.processor.cfg.HeartbeatInterval
	if interval < 15*time.Second {
		interval = 15 * time.Second
	}
	reconcile := func() {
		err := c.syncHostEnvironment(ctx)
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
	currentID := c.workspaceID
	if manifest.WorkspaceID != currentID {
		return errors.New("worker Workspace 绑定已变化，请重启宿主 Worker")
	}
	projects, scanErr := hostworker.ScanProjects(ctx, c.processor.cfg.WorkerWorkspaceRoot)
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
