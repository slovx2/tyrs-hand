package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

const codexRestartMarkerRelative = ".local/state/tyrs-hand/codex-restart-required"

func (m *Manager) CoordinateRemote(ctx context.Context,
	manifest workerprotocol.EnvironmentManifest,
) (Runtime, error) {
	runtime, err := m.PrepareRemoteRuntime(ctx, manifest)
	if err != nil {
		return Runtime{}, err
	}
	if err := m.EnsureRemoteDaemons(ctx, manifest, runtime); err != nil {
		return Runtime{}, err
	}
	return runtime, nil
}

func (m *Manager) PrepareRemoteRuntime(ctx context.Context,
	manifest workerprotocol.EnvironmentManifest,
) (Runtime, error) {
	if !m.Enabled() {
		return Runtime{}, errors.New("discord 开发容器未启用")
	}
	if manifest.EnvironmentID.String() == "00000000-0000-0000-0000-000000000000" ||
		manifest.ContainerName == "" || manifest.RuntimeUID <= 0 || manifest.RuntimeGID <= 0 ||
		manifest.RuntimeHome == "" {
		return Runtime{}, errors.New("开发环境 Manifest 不完整")
	}
	if _, err := m.docker(ctx, "start", manifest.ContainerName); err != nil {
		return Runtime{}, fmt.Errorf("启动常驻开发容器: %w", err)
	}
	runtimeDir := filepath.Join(m.developmentRuntimeDir, manifest.EnvironmentID.String())
	if err := os.MkdirAll(runtimeDir, 0o770); err != nil {
		return Runtime{}, err
	}
	hostSocket := filepath.Join(runtimeDir, "app-server.sock")
	return Runtime{EnvironmentID: manifest.EnvironmentID, Container: manifest.ContainerName,
		CodexHome: "/var/lib/tyrs-hand/codex", User: manifest.RuntimeUser,
		UID: manifest.RuntimeUID, GID: manifest.RuntimeGID, Home: manifest.RuntimeHome,
		AppServerSocket: hostSocket, RelaySocket: filepath.Join(runtimeDir, "relay.sock")}, nil
}

func (m *Manager) EnsureRemoteDaemons(ctx context.Context,
	manifest workerprotocol.EnvironmentManifest, runtime Runtime,
) error {
	hostSocket := runtime.AppServerSocket
	if !unixSocketAvailable(hostSocket) {
		operation := RemoteOperation{EnvironmentID: manifest.EnvironmentID,
			ContainerName: manifest.ContainerName, RuntimeUser: manifest.RuntimeUser,
			RuntimeUID: manifest.RuntimeUID, RuntimeGID: manifest.RuntimeGID,
			RuntimeHome: manifest.RuntimeHome, SSHPublicKey: manifest.SSHPublicKey,
			SSHPort: manifest.SSHPort, SSHConfigRevision: manifest.SSHConfigRevision,
			ProcessEnvironment: runtime.ProcessEnvironment}
		if err := m.configureRemoteDaemons(ctx, manifest.ContainerName, operation); err != nil {
			return err
		}
	} else if manifest.SSHPublicKey != "" {
		if err := m.ensureRemoteSSH(ctx, manifest.ContainerName); err != nil {
			return err
		}
	}
	return nil
}

func unixSocketAvailable(path string) bool {
	connection, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func (m *Manager) ensureRemoteSSH(ctx context.Context, container string) error {
	if _, err := m.docker(ctx, "exec", "--user", "0:0", container, "/bin/sh", "-c",
		`test -s /run/tyrs-hand/sshd.pid && kill -0 "$(cat /run/tyrs-hand/sshd.pid)"`); err == nil {
		return nil
	}
	if _, err := m.docker(ctx, "exec", "--detach", "--user", "0:0", container,
		"/usr/sbin/sshd", "-D", "-e", "-f",
		"/var/lib/tyrs-hand/system/ssh/sshd_config"); err != nil {
		return fmt.Errorf("恢复开发容器 SSH: %w", err)
	}
	return nil
}

func (m *Manager) CodexState(ctx context.Context, runtime Runtime) (string, bool, bool, error) {
	user := fmt.Sprintf("%d:%d", runtime.UID, runtime.GID)
	version, err := m.docker(ctx, "exec", "--user", user, "--env", "HOME="+runtime.Home,
		runtime.Container, "codex", "--version")
	if err != nil {
		return "", false, false, err
	}
	selection, err := m.docker(ctx, "exec", "--user", user, "--env", "HOME="+runtime.Home,
		runtime.Container, "tyrs-hand-dev", "codex", "status")
	if err != nil {
		return "", false, false, err
	}
	_, markerErr := m.docker(ctx, "exec", "--user", user, runtime.Container, "test", "-f",
		filepath.ToSlash(filepath.Join(runtime.Home, codexRestartMarkerRelative)))
	return strings.TrimSpace(version), strings.TrimSpace(selection) != "bundled", markerErr == nil, nil
}

func (m *Manager) ClearCodexRestartMarker(ctx context.Context, runtime Runtime) error {
	_, err := m.docker(ctx, "exec", "--user", fmt.Sprintf("%d:%d", runtime.UID, runtime.GID),
		runtime.Container, "rm", "-f", filepath.ToSlash(filepath.Join(runtime.Home,
			codexRestartMarkerRelative)))
	return err
}

func (m *Manager) RollbackUserCodex(ctx context.Context, runtime Runtime) error {
	arguments := []string{"exec", "--user", fmt.Sprintf("%d:%d", runtime.UID, runtime.GID),
		"--env", "HOME=" + runtime.Home, runtime.Container, "tyrs-hand-dev", "codex"}
	_, err := m.docker(ctx, append(arguments, "rollback")...)
	return err
}

func (m *Manager) ResetUserCodex(ctx context.Context, runtime Runtime) error {
	arguments := []string{"exec", "--user", fmt.Sprintf("%d:%d", runtime.UID, runtime.GID),
		"--env", "HOME=" + runtime.Home, runtime.Container, "tyrs-hand-dev", "codex", "reset"}
	_, err := m.docker(ctx, arguments...)
	return err
}
