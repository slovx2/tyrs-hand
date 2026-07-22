package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	containerRunDir = "/run/tyrs-hand"
	appServerSocket = containerRunDir + "/app-server.sock"
)

func (m *Manager) reconfigureRemote(ctx context.Context, operation RemoteOperation) error {
	if operation.EnvironmentID.String() == "00000000-0000-0000-0000-000000000000" ||
		operation.ContainerName == "" || operation.ImageRef == "" || operation.RuntimeUID <= 0 ||
		operation.RuntimeGID <= 0 || operation.RuntimeHome == "" {
		return errors.New("开发环境 reconfigure 参数不完整")
	}
	if (operation.SSHPublicKey == "") != (operation.SSHPort == 0) {
		return errors.New("必须同时配置 SSH 公钥与端口")
	}
	runtimeDir := filepath.Join(m.developmentRuntimeDir, operation.EnvironmentID.String())
	if err := os.MkdirAll(runtimeDir, 0o770); err != nil {
		return fmt.Errorf("创建环境运行目录: %w", err)
	}
	if err := os.Chmod(runtimeDir, 0o770); err != nil {
		return fmt.Errorf("设置环境运行目录权限: %w", err)
	}
	hostRuntimeDir := filepath.Join(m.developmentRuntimeHostDir, operation.EnvironmentID.String())
	candidate := operation.ContainerName + "-candidate-" + time.Now().UTC().Format("20060102150405.000000000")
	_, _ = m.docker(ctx, "rm", "--force", candidate)
	oldExists := m.dockerResourceExists(ctx, "container", operation.ContainerName)
	if oldExists {
		if _, err := m.docker(ctx, "stop", "--time", "10", operation.ContainerName); err != nil {
			return fmt.Errorf("停止旧开发容器: %w", err)
		}
	}
	restoreOld := func() {
		_, _ = m.docker(context.Background(), "rm", "--force", candidate)
		if oldExists {
			_, _ = m.docker(context.Background(), "start", operation.ContainerName)
		}
	}
	if _, err := m.docker(ctx, m.remoteContainerCreateArguments(operation, candidate, hostRuntimeDir)...); err != nil {
		restoreOld()
		return fmt.Errorf("创建重配开发容器: %w", err)
	}
	if _, err := m.docker(ctx, "start", candidate); err != nil {
		restoreOld()
		return fmt.Errorf("启动重配开发容器: %w", err)
	}
	if err := m.installRuntime(ctx, candidate, operation.RuntimeUID, operation.RuntimeGID,
		operation.RuntimeHome); err != nil {
		restoreOld()
		return fmt.Errorf("安装重配容器 Codex 运行时: %w", err)
	}
	if err := m.configureRemoteDaemons(ctx, candidate, operation); err != nil {
		restoreOld()
		return err
	}
	backup := ""
	if oldExists {
		backup = operation.ContainerName + "-previous-" + time.Now().UTC().Format("20060102150405.000000000")
		if _, err := m.docker(ctx, "rename", operation.ContainerName, backup); err != nil {
			restoreOld()
			return fmt.Errorf("保留旧开发容器: %w", err)
		}
	}
	if _, err := m.docker(ctx, "rename", candidate, operation.ContainerName); err != nil {
		if backup != "" {
			_, _ = m.docker(context.Background(), "rename", backup, operation.ContainerName)
		}
		restoreOld()
		return fmt.Errorf("切换重配开发容器: %w", err)
	}
	if backup != "" {
		_, _ = m.docker(context.Background(), "rm", "--force", backup)
	}
	return nil
}

func (m *Manager) remoteContainerCreateArguments(operation RemoteOperation, name,
	hostRuntimeDir string,
) []string {
	arguments := []string{"create", "--name", name, "--restart", "unless-stopped",
		"--label", "com.tyrs-hand.development-environment=" + operation.EnvironmentID.String(),
		"--network", operation.Network,
		"--volume", operation.DataVolume + ":" + containerRoot,
		"--volume", operation.HomeVolume + ":" + operation.RuntimeHome,
		"--mount", "type=bind,source=" + hostRuntimeDir + ",target=" + containerRunDir,
		"--add-host", "host.docker.internal:host-gateway"}
	if operation.SSHPort > 0 {
		arguments = append(arguments, "--publish", strconv.Itoa(operation.SSHPort)+":22")
	}
	if m.sshEnabled {
		arguments = append(arguments, "--mount", "type=bind,source="+
			m.sshAgentHostDir+",target="+m.sshAgentDir)
	}
	if m.browserEnabled {
		arguments = append(arguments, "--mount", "type=bind,source="+
			m.browserFilesHostRoot+",target="+m.browserFilesRoot)
	}
	return append(arguments, "--entrypoint", "/bin/sh", operation.ImageRef,
		"-c", "while :; do sleep 3600; done")
}

func (m *Manager) configureRemoteDaemons(ctx context.Context, container string,
	operation RemoteOperation,
) error {
	owner := fmt.Sprintf("%d:%d", operation.RuntimeUID, operation.RuntimeGID)
	setup := `set -eu
install -d -m 0770 /run/tyrs-hand
chown "$TYRS_OWNER" /run/tyrs-hand
chmod 0700 /run/tyrs-hand
install -d -m 0755 /run/sshd
install -d -o "$TYRS_UID" -g "$TYRS_GID" -m 0700 /var/lib/tyrs-hand/codex
if test -s /run/tyrs-hand/app-server.pid && kill -0 "$(cat /run/tyrs-hand/app-server.pid)" 2>/dev/null; then
  kill "$(cat /run/tyrs-hand/app-server.pid)" || true
  n=0
  while kill -0 "$(cat /run/tyrs-hand/app-server.pid)" 2>/dev/null && test "$n" -lt 50; do n=$((n + 1)); sleep 0.1; done
fi
rm -f /run/tyrs-hand/app-server.pid
rm -f /run/tyrs-hand/app-server.sock
if test -n "$TYRS_SSH_PUBLIC_KEY"; then
  install -d -m 0700 /var/lib/tyrs-hand/system/ssh
  test -f /var/lib/tyrs-hand/system/ssh/ssh_host_ed25519_key || ssh-keygen -q -t ed25519 -N '' -f /var/lib/tyrs-hand/system/ssh/ssh_host_ed25519_key
  install -d -o "$TYRS_UID" -g "$TYRS_GID" -m 0700 "$TYRS_HOME/.ssh"
  printf '%s\n' "$TYRS_SSH_PUBLIC_KEY" > "$TYRS_HOME/.ssh/authorized_keys"
  chown "$TYRS_OWNER" "$TYRS_HOME/.ssh/authorized_keys"
  chmod 0600 "$TYRS_HOME/.ssh/authorized_keys"
  printf '%s\n' "$TYRS_SSHD_CONFIG" > /var/lib/tyrs-hand/system/ssh/sshd_config
  chmod 0600 /var/lib/tyrs-hand/system/ssh/ssh_host_ed25519_key
fi`
	config := remoteSSHDConfig(operation.RuntimeUser)
	if _, err := m.docker(ctx, "exec", "--user", "0:0",
		"--env", "TYRS_UID="+strconv.FormatInt(operation.RuntimeUID, 10),
		"--env", "TYRS_GID="+strconv.FormatInt(operation.RuntimeGID, 10),
		"--env", "TYRS_OWNER="+owner, "--env", "TYRS_HOME="+operation.RuntimeHome,
		"--env", "TYRS_SSH_PUBLIC_KEY="+operation.SSHPublicKey,
		"--env", "TYRS_SSHD_CONFIG="+config, container, "/bin/sh", "-c", setup); err != nil {
		return fmt.Errorf("配置开发容器 daemon: %w", err)
	}
	if operation.SSHPublicKey != "" {
		if _, err := m.docker(ctx, "exec", "--detach", "--user", "0:0", container,
			"/usr/sbin/sshd", "-D", "-e", "-f", "/var/lib/tyrs-hand/system/ssh/sshd_config"); err != nil {
			return fmt.Errorf("启动开发容器 SSH: %w", err)
		}
	}
	appServerCommand := `set -eu
echo $$ > /run/tyrs-hand/app-server.pid
exec /opt/tyrs-hand/libexec/codex-real app-server --listen unix:///run/tyrs-hand/app-server.sock \
  >>/run/tyrs-hand/app-server.log 2>&1`
	if _, err := m.docker(ctx, "exec", "--detach", "--user", owner,
		"--env", "HOME="+operation.RuntimeHome, "--env", "CODEX_HOME=/var/lib/tyrs-hand/codex",
		container, "/bin/sh", "-c", appServerCommand); err != nil {
		return fmt.Errorf("启动环境 Codex app-server: %w", err)
	}
	if err := m.waitForAppServerSocket(ctx, container); err != nil {
		return err
	}
	return m.shareAppServerSocket(ctx, container, operation.EnvironmentID)
}

func (m *Manager) shareAppServerSocket(ctx context.Context, container string,
	environmentID uuid.UUID,
) error {
	// Codex 启动时要求 Socket 目录属于自己且权限为 0700；监听完成后再恢复 Worker
	// 对宿主 bind 的所有权。宿主父目录仍是 0770，只对当前 Worker 开放。
	workerOwner := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	script := `set -eu
chown "$TYRS_WORKER_OWNER" /run/tyrs-hand
chmod 0777 /run/tyrs-hand`
	if _, err := m.docker(ctx, "exec", "--user", "0:0",
		"--env", "TYRS_WORKER_OWNER="+workerOwner,
		container, "/bin/sh", "-c", script); err != nil {
		return fmt.Errorf("共享环境 Codex app-server Socket: %w", err)
	}
	if _, err := m.docker(ctx, "exec", "--user", "0:0", container,
		"chmod", "0666", appServerSocket); err != nil {
		// Docker Desktop 的 bind mount 不允许容器修改 Unix Socket 权限，但宿主进程可以。
		hostSocket := filepath.Join(m.developmentRuntimeDir, environmentID.String(),
			filepath.Base(appServerSocket))
		if hostErr := os.Chmod(hostSocket, 0o666); hostErr != nil {
			return fmt.Errorf("设置环境 Codex app-server Socket 权限: %w（宿主回退: %v）",
				err, hostErr)
		}
	}
	return nil
}

func (m *Manager) StopRemoteAppServer(ctx context.Context, container string) error {
	script := `set -eu
if test -s /run/tyrs-hand/app-server.pid && kill -0 "$(cat /run/tyrs-hand/app-server.pid)" 2>/dev/null; then
  kill "$(cat /run/tyrs-hand/app-server.pid)"
fi
rm -f /run/tyrs-hand/app-server.pid /run/tyrs-hand/app-server.sock`
	_, err := m.docker(ctx, "exec", "--user", "0:0", container, "/bin/sh", "-c", script)
	return err
}

func (m *Manager) waitForAppServerSocket(ctx context.Context, container string) error {
	for attempt := 0; attempt < 50; attempt++ {
		if _, err := m.docker(ctx, "exec", container, "test", "-S", appServerSocket); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("app-server socket 未就绪")
}

func remoteSSHDConfig(runtimeUser string) string {
	return strings.Join([]string{
		"Port 22", "HostKey /var/lib/tyrs-hand/system/ssh/ssh_host_ed25519_key",
		"PidFile /run/tyrs-hand/sshd.pid", "AuthorizedKeysFile .ssh/authorized_keys",
		"AuthenticationMethods publickey", "PubkeyAuthentication yes",
		"PasswordAuthentication no", "KbdInteractiveAuthentication no",
		"PermitRootLogin no", "PermitEmptyPasswords no", "UsePAM yes",
		"DisableForwarding yes", "X11Forwarding no", "PermitTunnel no",
		"AllowUsers " + runtimeUser,
		"Subsystem sftp /usr/lib/openssh/sftp-server",
	}, "\n")
}
