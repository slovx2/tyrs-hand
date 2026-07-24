package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (p *RemoteProcessor) CoordinateEnvironments(ctx context.Context) error {
	if p.development == nil || !p.development.Enabled() {
		return nil
	}
	manifests, err := p.client.DevelopmentEnvironments(ctx)
	if err != nil {
		return err
	}
	active := make(map[uuid.UUID]bool, len(manifests))
	var failures []error
	for index := range manifests {
		manifest := &manifests[index]
		active[manifest.EnvironmentID] = true
		if err := p.coordinateEnvironment(ctx, manifest); err != nil {
			failures = append(failures, fmt.Errorf("环境 %s: %w", manifest.EnvironmentID, err))
		}
	}
	p.environments.retain(active)
	if err := p.applyPendingThreadNames(ctx); err != nil {
		failures = append(failures, err)
	}
	if err := p.applyPendingThreadLifecycles(ctx); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (p *RemoteProcessor) applyPendingThreadLifecycles(ctx context.Context) error {
	updates, err := p.client.PendingThreadLifecycles(ctx)
	if err != nil {
		return fmt.Errorf("读取待应用 Thread lifecycle: %w", err)
	}
	var failures []error
	for _, update := range updates {
		entry := p.environments.get(update.EnvironmentID)
		if entry == nil {
			continue
		}
		method := "thread/unarchive"
		if update.DesiredState == "archived" {
			method = "thread/archive"
		}
		var response json.RawMessage
		requestCtx, cancel := context.WithTimeout(ctx, p.cfg.ControlTimeout)
		callErr := entry.client.Call(requestCtx, method, map[string]any{
			"threadId": update.ThreadID,
		}, &response)
		cancel()
		ack := workerprotocol.ThreadLifecycleCompleteRequest{
			EnvironmentID: update.EnvironmentID,
			Response:      response,
		}
		if callErr != nil {
			ack.Error = callErr.Error()
			failures = append(failures, fmt.Errorf("应用 Thread %s lifecycle: %w",
				update.ThreadID, callErr))
		}
		ackCtx, ackCancel := context.WithTimeout(ctx, p.cfg.ControlTimeout)
		ackErr := p.client.CompleteThreadLifecycle(ackCtx, update.ID, ack)
		ackCancel()
		if ackErr != nil {
			failures = append(failures, fmt.Errorf("确认 Thread %s lifecycle: %w",
				update.ThreadID, ackErr))
		}
	}
	return errors.Join(failures...)
}

func (p *RemoteProcessor) applyPendingThreadNames(ctx context.Context) error {
	updates, err := p.client.PendingThreadNames(ctx)
	if err != nil {
		return fmt.Errorf("读取待应用 Thread 标题: %w", err)
	}
	var failures []error
	for _, update := range updates {
		entry := p.environments.get(update.EnvironmentID)
		if entry == nil {
			continue
		}
		requestCtx, cancel := context.WithTimeout(ctx, p.cfg.ControlTimeout)
		callErr := entry.client.Call(requestCtx, "thread/name/set", map[string]any{
			"threadId": update.ThreadID, "name": update.Name,
		}, nil)
		cancel()
		ack := workerprotocol.ThreadNameAckRequest{EnvironmentID: update.EnvironmentID,
			Revision: update.Revision}
		if callErr != nil {
			ack.Error = callErr.Error()
			failures = append(failures, fmt.Errorf("应用 Thread %s 标题: %w",
				update.ThreadID, callErr))
		}
		ackCtx, ackCancel := context.WithTimeout(ctx, p.cfg.ControlTimeout)
		ackErr := p.client.AckThreadName(ackCtx, update.ControlID, ack)
		ackCancel()
		if ackErr != nil {
			failures = append(failures, fmt.Errorf("确认 Thread %s 标题: %w",
				update.ThreadID, ackErr))
		}
	}
	return errors.Join(failures...)
}

func (p *RemoteProcessor) coordinateEnvironment(ctx context.Context,
	manifest *workerprotocol.EnvironmentManifest,
) error {
	unlock := devcontainer.LockRemoteEnvironment(manifest.EnvironmentID)
	defer unlock()
	state := workerprotocol.EnvironmentDaemonState{EnvironmentID: manifest.EnvironmentID,
		Status: "starting", AppServerStatus: "starting", RelayStatus: "starting",
		SSHStatus: environmentSSHState(manifest, "starting")}
	_ = p.client.EnvironmentDaemonState(ctx, state)
	runtime, err := p.development.PrepareRemoteRuntime(ctx, *manifest)
	appServerRestart := err == nil && !environmentSocketAvailable(runtime.AppServerSocket)
	if err == nil && p.environments.idle(manifest.EnvironmentID) {
		var changed bool
		changed, err = p.development.RefreshRemoteRuntime(ctx, runtime)
		if changed {
			appServerRestart = true
		}
	}
	if err == nil {
		var credential workerprotocol.RuntimeCredential
		credential, err = p.client.EnvironmentRuntimeCredential(ctx, manifest.EnvironmentID)
		if err == nil {
			runtime.ModelSource = credential.ModelSource
			runtime.ModelBaseURL = credential.BaseURL
			runtime.ProcessEnvironment, err = remoteCredentialEnvironment(credential)
		}
		if err == nil {
			var changed bool
			changed, err = p.installEnvironmentCodexHome(ctx, runtime, credential)
			if err == nil && changed {
				appServerRestart = true
				err = p.development.StopRemoteAppServer(ctx, runtime.Container)
			}
		}
	}
	if err == nil {
		err = p.development.EnsureRemoteDaemons(ctx, *manifest, runtime)
	}
	if err == nil && appServerRestart {
		err = p.client.InterruptEnvironmentInteractive(ctx, manifest.EnvironmentID)
	}
	if err == nil {
		_, err = p.environments.ensure(runtime, manifest)
	}
	if err != nil {
		state.Status, state.Error = "error", err.Error()
		state.AppServerStatus, state.RelayStatus = "error", "error"
		state.SSHStatus = environmentSSHState(manifest, "error")
		_ = p.client.EnvironmentDaemonState(context.Background(), state)
		return err
	}
	state.Status, state.AppServerStatus, state.RelayStatus = "running", "running", "running"
	state.SSHStatus = environmentSSHState(manifest, "running")
	return p.client.EnvironmentDaemonState(ctx, state)
}

func environmentSSHState(manifest *workerprotocol.EnvironmentManifest, state string) string {
	if manifest.SSHPublicKey == "" {
		return "disabled"
	}
	return state
}

func environmentSocketAvailable(path string) bool {
	connection, err := net.DialTimeout("unix", path, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func (p *RemoteProcessor) installEnvironmentCodexHome(ctx context.Context,
	runtime devcontainer.Runtime, credential workerprotocol.RuntimeCredential,
) (bool, error) {
	marker := filepath.Join(filepath.Dir(runtime.AppServerSocket), "codex-config-signature")
	current, markerErr := os.ReadFile(marker)
	if markerErr == nil && string(current) == credential.ConfigSignature {
		return false, nil
	}
	if !p.environments.idle(runtime.EnvironmentID) {
		// 已连接的 Desktop 或活动工具上下文优先完成，配置在下一轮协调时生效。
		return false, nil
	}
	temporaryRoot := filepath.Join(p.cfg.WorkerDataRoot, "tmp")
	if err := os.MkdirAll(temporaryRoot, 0o700); err != nil {
		return false, err
	}
	temporaryHome, err := os.MkdirTemp(temporaryRoot, "environment-codex-home-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(temporaryHome) }()
	if _, err := prepareRemoteCodexHome(temporaryHome, credential, credential.GlobalAgents); err != nil {
		return false, err
	}
	if err := p.development.CopyToRuntime(ctx, runtime, temporaryHome, runtime.CodexHome); err != nil {
		return false, err
	}
	if err := os.WriteFile(marker, []byte(credential.ConfigSignature), 0o600); err != nil {
		return false, err
	}
	return true, nil
}
