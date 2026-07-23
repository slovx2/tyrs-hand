package worker

import (
	"context"
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
