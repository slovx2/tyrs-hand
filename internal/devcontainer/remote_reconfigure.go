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
	"github.com/slovx2/tyrs-hand/internal/codex"
)

const (
	containerRunDir             = "/run/tyrs-hand"
	containerBrowserServicesDir = "/run/tyrs-hand-browser-services"
	appServerSocket             = containerRunDir + "/app-server.sock"
	stopRemoteAppServersScript  = `tyrs_stop_app_server_pid() {
  TYRS_APP_SERVER_PID="$1"
  if ! kill -0 "$TYRS_APP_SERVER_PID" 2>/dev/null; then
    return
  fi
  kill "$TYRS_APP_SERVER_PID" || true
  n=0
  while kill -0 "$TYRS_APP_SERVER_PID" 2>/dev/null && test "$n" -lt 50; do n=$((n + 1)); sleep 0.1; done
  if kill -0 "$TYRS_APP_SERVER_PID" 2>/dev/null; then
    kill -KILL "$TYRS_APP_SERVER_PID" || true
    n=0
    while kill -0 "$TYRS_APP_SERVER_PID" 2>/dev/null && test "$n" -lt 50; do n=$((n + 1)); sleep 0.1; done
  fi
  if kill -0 "$TYRS_APP_SERVER_PID" 2>/dev/null; then
    echo "app-server 未退出: $TYRS_APP_SERVER_PID" >&2
    exit 1
  fi
}
if test -s /run/tyrs-hand/app-server.pid; then
  tyrs_stop_app_server_pid "$(cat /run/tyrs-hand/app-server.pid)"
fi
for TYRS_APP_SERVER_CMDLINE in /proc/[0-9]*/cmdline; do
  if tr '\000' '\n' < "$TYRS_APP_SERVER_CMDLINE" 2>/dev/null |
      grep -Fxq "unix:///run/tyrs-hand/app-server.sock"; then
    TYRS_APP_SERVER_PID="${TYRS_APP_SERVER_CMDLINE#/proc/}"
    tyrs_stop_app_server_pid "${TYRS_APP_SERVER_PID%/cmdline}"
  fi
done`
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
	oldExists := m.dockerResourceExists(ctx, "container", operation.ContainerName)
	if oldExists && operation.Operation == "reconfigure" {
		currentPort, err := m.remoteSSHPort(ctx, operation.ContainerName)
		if err != nil {
			return fmt.Errorf("读取开发容器 SSH 端口: %w", err)
		}
		if currentPort == operation.SSHPort {
			return m.configureRemoteSSH(ctx, operation.ContainerName, operation)
		}
	}
	runtimeDir := filepath.Join(m.developmentRuntimeDir, operation.EnvironmentID.String())
	if err := os.MkdirAll(runtimeDir, 0o770); err != nil {
		return fmt.Errorf("创建环境运行目录: %w", err)
	}
	if err := os.Chmod(runtimeDir, 0o770); err != nil {
		return fmt.Errorf("设置环境运行目录权限: %w", err)
	}
	hostRuntimeDir := filepath.Join(m.developmentRuntimeHostDir, operation.EnvironmentID.String())
	if err := m.prepareBrowserServicesDirectory(operation.EnvironmentID); err != nil {
		return err
	}
	candidate := operation.ContainerName + "-candidate-" + time.Now().UTC().Format("20060102150405.000000000")
	_, _ = m.docker(ctx, "rm", "--force", candidate)
	if oldExists {
		if _, err := m.docker(ctx, "stop", "--time", "10", operation.ContainerName); err != nil {
			return fmt.Errorf("停止旧开发容器: %w", err)
		}
	}
	restoreOld := func() {
		_, _ = m.docker(context.Background(), "rm", "--force", candidate)
		if oldExists {
			_, _ = m.docker(context.Background(), "start", operation.ContainerName)
			_ = m.configureRemoteDaemons(context.Background(), operation.ContainerName, operation)
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

func (m *Manager) remoteSSHPort(ctx context.Context, container string) (int, error) {
	networkMode, err := m.docker(ctx, "inspect", "--format", `{{.HostConfig.NetworkMode}}`, container)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(networkMode) == "host" {
		value, labelErr := m.docker(ctx, "inspect", "--format",
			`{{index .Config.Labels "com.tyrs-hand.ssh-port"}}`, container)
		if labelErr != nil {
			return 0, labelErr
		}
		return parseRemoteSSHPort(value)
	}
	value, err := m.docker(ctx, "inspect", "--format",
		`{{with (index .NetworkSettings.Ports "22/tcp")}}{{(index . 0).HostPort}}{{end}}`, container)
	if err != nil {
		return 0, err
	}
	return parseRemoteSSHPort(value)
}

func parseRemoteSSHPort(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, errors.New("docker 返回了无效的 SSH 宿主端口")
	}
	return port, nil
}

func (m *Manager) containerSSHPort(ctx context.Context, container string, assignedPort int) (int, error) {
	if assignedPort == 0 {
		return 22, nil
	}
	networkMode, err := m.docker(ctx, "inspect", "--format", `{{.HostConfig.NetworkMode}}`, container)
	if err != nil {
		return 0, fmt.Errorf("读取开发容器网络模式: %w", err)
	}
	if strings.TrimSpace(networkMode) == "host" {
		return assignedPort, nil
	}
	return 22, nil
}

func (m *Manager) appendDevelopmentDockerArguments(arguments []string, network string,
	sshPort int,
) []string {
	if !m.hostDocker {
		arguments = append(arguments, "--network", network,
			"--add-host", "host.docker.internal:host-gateway")
		if sshPort > 0 {
			arguments = append(arguments, "--publish", strconv.Itoa(sshPort)+":22")
		}
		return arguments
	}
	arguments = append(arguments,
		"--network", "host",
		"--mount", "type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock",
		"--group-add", strconv.FormatUint(uint64(m.dockerSocketGID), 10))
	if sshPort > 0 {
		arguments = append(arguments, "--label", "com.tyrs-hand.ssh-port="+strconv.Itoa(sshPort))
	}
	return arguments
}

func (m *Manager) configureRemoteSSH(ctx context.Context, container string,
	operation RemoteOperation,
) error {
	sshPort, err := m.containerSSHPort(ctx, container, operation.SSHPort)
	if err != nil {
		return err
	}
	owner := fmt.Sprintf("%d:%d", operation.RuntimeUID, operation.RuntimeGID)
	script := `set -eu
if test -s /run/tyrs-hand/sshd.pid && kill -0 "$(cat /run/tyrs-hand/sshd.pid)" 2>/dev/null; then
  kill "$(cat /run/tyrs-hand/sshd.pid)"
fi
rm -f /run/tyrs-hand/sshd.pid
if test -z "$TYRS_SSH_PUBLIC_KEY"; then
  rm -f "$TYRS_HOME/.ssh/authorized_keys"
  exit 0
fi
install -d -m 0755 /run/sshd
install -d -m 0700 /var/lib/tyrs-hand/system/ssh
test -f /var/lib/tyrs-hand/system/ssh/ssh_host_ed25519_key || ssh-keygen -q -t ed25519 -N '' -f /var/lib/tyrs-hand/system/ssh/ssh_host_ed25519_key
install -d -o "$TYRS_UID" -g "$TYRS_GID" -m 0700 "$TYRS_HOME/.ssh"
printf '%s\n' "$TYRS_SSH_PUBLIC_KEY" > "$TYRS_HOME/.ssh/authorized_keys"
chown "$TYRS_OWNER" "$TYRS_HOME/.ssh/authorized_keys"
chmod 0600 "$TYRS_HOME/.ssh/authorized_keys"
printf '%s\n' "$TYRS_SSHD_CONFIG" > /var/lib/tyrs-hand/system/ssh/sshd_config
chmod 0600 /var/lib/tyrs-hand/system/ssh/ssh_host_ed25519_key`
	if _, err := m.docker(ctx, "exec", "--user", "0:0",
		"--env", "TYRS_UID="+strconv.FormatInt(operation.RuntimeUID, 10),
		"--env", "TYRS_GID="+strconv.FormatInt(operation.RuntimeGID, 10),
		"--env", "TYRS_OWNER="+owner, "--env", "TYRS_HOME="+operation.RuntimeHome,
		"--env", "TYRS_SSH_PUBLIC_KEY="+operation.SSHPublicKey,
		"--env", "TYRS_SSHD_CONFIG="+remoteSSHDConfig(operation.RuntimeUser, sshPort),
		container, "/bin/sh", "-c", script); err != nil {
		return fmt.Errorf("更新开发容器 SSH: %w", err)
	}
	if operation.SSHPublicKey == "" {
		return nil
	}
	if _, err := m.docker(ctx, "exec", "--detach", "--user", "0:0", container,
		"/usr/sbin/sshd", "-D", "-e", "-f", "/var/lib/tyrs-hand/system/ssh/sshd_config"); err != nil {
		return fmt.Errorf("启动开发容器 SSH: %w", err)
	}
	return nil
}

func (m *Manager) remoteContainerCreateArguments(operation RemoteOperation, name,
	hostRuntimeDir string,
) []string {
	arguments := []string{"create", "--name", name, "--restart", "unless-stopped",
		"--label", "com.tyrs-hand.development-environment=" + operation.EnvironmentID.String(),
		"--volume", operation.DataVolume + ":" + containerRoot,
		"--volume", operation.HomeVolume + ":" + operation.RuntimeHome,
		"--mount", "type=bind,source=" + hostRuntimeDir + ",target=" + containerRunDir}
	arguments = m.appendDevelopmentDockerArguments(arguments, operation.Network, operation.SSHPort)
	if m.sshEnabled {
		arguments = append(arguments, "--mount", "type=bind,source="+
			m.sshAgentHostDir+",target="+m.sshAgentDir,
			"--env", "SSH_AUTH_SOCK="+filepath.Join(m.sshAgentDir, "current.sock"))
	}
	if m.browserEnabled {
		arguments = append(arguments, "--mount", "type=bind,source="+
			m.browserFilesHostRoot+",target="+m.browserFilesRoot,
			"--mount", "type=bind,source="+filepath.Join(m.browserServicesHostRoot,
				operation.EnvironmentID.String())+",target="+containerBrowserServicesDir)
	}
	return append(arguments, "--entrypoint", "/bin/sh", operation.ImageRef,
		"-c", "while :; do sleep 3600; done")
}

func (m *Manager) configureRemoteDaemons(ctx context.Context, container string,
	operation RemoteOperation,
) error {
	sshPort, err := m.containerSSHPort(ctx, container, operation.SSHPort)
	if err != nil {
		return err
	}
	owner := fmt.Sprintf("%d:%d", operation.RuntimeUID, operation.RuntimeGID)
	setup := `set -eu
install -d -m 0770 /run/tyrs-hand
chown "$TYRS_OWNER" /run/tyrs-hand
chmod 0700 /run/tyrs-hand
install -d -m 0755 /run/sshd
install -d -o "$TYRS_UID" -g "$TYRS_GID" -m 0700 /var/lib/tyrs-hand/codex
if test -n "${TYRS_DOCKER_GID:-}"; then
  TYRS_DOCKER_GROUP="$(getent group "$TYRS_DOCKER_GID" | cut -d: -f1 || true)"
  if test -z "$TYRS_DOCKER_GROUP"; then
    TYRS_DOCKER_GROUP="tyrs-docker-$TYRS_DOCKER_GID"
    groupadd --gid "$TYRS_DOCKER_GID" "$TYRS_DOCKER_GROUP"
  fi
  usermod --append --groups "$TYRS_DOCKER_GROUP" "$TYRS_RUNTIME_USER"
fi
` + stopRemoteAppServersScript + `
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
	config := remoteSSHDConfig(operation.RuntimeUser, sshPort)
	setupArguments := []string{"exec", "--user", "0:0",
		"--env", "TYRS_UID=" + strconv.FormatInt(operation.RuntimeUID, 10),
		"--env", "TYRS_GID=" + strconv.FormatInt(operation.RuntimeGID, 10),
		"--env", "TYRS_OWNER=" + owner, "--env", "TYRS_HOME=" + operation.RuntimeHome,
		"--env", "TYRS_SSH_PUBLIC_KEY=" + operation.SSHPublicKey,
		"--env", "TYRS_SSHD_CONFIG=" + config}
	if m.hostDocker {
		setupArguments = append(setupArguments,
			"--env", "TYRS_DOCKER_GID="+strconv.FormatUint(uint64(m.dockerSocketGID), 10),
			"--env", "TYRS_RUNTIME_USER="+operation.RuntimeUser)
	}
	setupArguments = append(setupArguments, container, "/bin/sh", "-c", setup)
	if _, err := m.docker(ctx, setupArguments...); err != nil {
		return fmt.Errorf("配置开发容器 daemon: %w", err)
	}
	if m.sshEnabled {
		if err := m.installSSHConfiguration(ctx, container, operation.RuntimeHome, owner); err != nil {
			return fmt.Errorf("安装开发容器 SSH 客户端配置: %w", err)
		}
	}
	if operation.SSHPublicKey != "" {
		if _, err := m.docker(ctx, "exec", "--detach", "--user", "0:0", container,
			"/usr/sbin/sshd", "-D", "-e", "-f", "/var/lib/tyrs-hand/system/ssh/sshd_config"); err != nil {
			return fmt.Errorf("启动开发容器 SSH: %w", err)
		}
	}
	if m.browserEnabled {
		if err := m.startBrowserServiceProxy(ctx, container, owner); err != nil {
			return err
		}
	}
	appServerCommand := `set -eu
	echo $$ > /run/tyrs-hand/app-server.pid
	exec "$@" >>/run/tyrs-hand/app-server.log 2>&1`
	arguments := []string{"exec", "--detach", "--user", owner,
		"--env", "HOME=" + operation.RuntimeHome,
		"--env", "CODEX_HOME=/var/lib/tyrs-hand/codex"}
	if operation.AppServerConfig.ModelCatalogPath != "" {
		appServerCommand = `set -eu
		codex debug models --bundled > "$TYRS_MODEL_CATALOG_PATH.tmp"
		chmod 0600 "$TYRS_MODEL_CATALOG_PATH.tmp"
		mv "$TYRS_MODEL_CATALOG_PATH.tmp" "$TYRS_MODEL_CATALOG_PATH"
		echo $$ > /run/tyrs-hand/app-server.pid
		exec "$@" >>/run/tyrs-hand/app-server.log 2>&1`
		arguments = append(arguments, "--env",
			"TYRS_MODEL_CATALOG_PATH="+operation.AppServerConfig.ModelCatalogPath)
	}
	for _, entry := range operation.ProcessEnvironment {
		arguments = append(arguments, "--env", entry)
	}
	arguments = append(arguments, container, "/bin/sh", "-c", appServerCommand,
		"tyrs-hand-app-server", "codex")
	arguments = append(arguments,
		codex.ManagedAppServerArguments("unix:///run/tyrs-hand/app-server.sock",
			operation.AppServerConfig)...)
	if _, err := m.docker(ctx, arguments...); err != nil {
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
` + stopRemoteAppServersScript + `
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

func remoteSSHDConfig(runtimeUser string, port int) string {
	return strings.Join([]string{
		"Port " + strconv.Itoa(port), "HostKey /var/lib/tyrs-hand/system/ssh/ssh_host_ed25519_key",
		"PidFile /run/tyrs-hand/sshd.pid", "AuthorizedKeysFile .ssh/authorized_keys",
		"AuthenticationMethods publickey", "PubkeyAuthentication yes",
		"PasswordAuthentication no", "KbdInteractiveAuthentication no",
		"PermitRootLogin no", "PermitEmptyPasswords no", "UsePAM yes",
		"AllowTcpForwarding local", "PermitOpen 127.0.0.1:*",
		"GatewayPorts no", "X11Forwarding no", "PermitTunnel no",
		"AllowUsers " + runtimeUser,
		"Subsystem sftp /usr/lib/openssh/sftp-server",
	}, "\n")
}

func (m *Manager) prepareBrowserServicesDirectory(environmentID uuid.UUID) error {
	if !m.browserEnabled {
		return nil
	}
	directory := filepath.Join(m.browserServicesRoot, environmentID.String())
	if err := os.MkdirAll(filepath.Dir(directory), 0o770); err != nil {
		return fmt.Errorf("创建浏览器服务转发目录: %w", err)
	}
	if err := os.Mkdir(directory, 0o770); err != nil {
		if errors.Is(err, os.ErrExist) {
			info, statErr := os.Stat(directory)
			if statErr != nil {
				return fmt.Errorf("检查浏览器服务转发目录: %w", statErr)
			}
			if !info.IsDir() {
				return errors.New("浏览器服务转发路径不是目录")
			}
			// 开发容器会把这个 bind 目录交给运行用户；Worker 后续不得再修改所有权或权限。
			return nil
		}
		return fmt.Errorf("创建浏览器服务转发目录: %w", err)
	}
	if err := os.Chmod(directory, 0o770); err != nil {
		return fmt.Errorf("设置浏览器服务转发目录权限: %w", err)
	}
	return nil
}

func (m *Manager) startBrowserServiceProxy(ctx context.Context, container, owner string) error {
	setup := `set -eu
chown "$TYRS_OWNER" /run/tyrs-hand-browser-services
chmod 0770 /run/tyrs-hand-browser-services
if test -s /run/tyrs-hand-browser-services/proxy.pid &&
  kill -0 "$(cat /run/tyrs-hand-browser-services/proxy.pid)" 2>/dev/null; then
  kill "$(cat /run/tyrs-hand-browser-services/proxy.pid)" || true
fi
rm -f /run/tyrs-hand-browser-services/proxy.pid /run/tyrs-hand-browser-services/proxy.sock`
	if _, err := m.docker(ctx, "exec", "--user", "0:0", "--env",
		"TYRS_OWNER="+owner, container, "/bin/sh", "-c", setup); err != nil {
		return fmt.Errorf("准备开发环境服务代理: %w", err)
	}
	script := `set -eu
echo $$ > /run/tyrs-hand-browser-services/proxy.pid
exec tyrs-hand-dev service proxy >>/run/tyrs-hand-browser-services/proxy.log 2>&1`
	if _, err := m.docker(ctx, "exec", "--detach", "--user", owner,
		container, "/bin/sh", "-c", script); err != nil {
		return fmt.Errorf("启动开发环境服务代理: %w", err)
	}
	for attempt := 0; attempt < 50; attempt++ {
		if _, err := m.docker(ctx, "exec", container, "test", "-S",
			"/run/tyrs-hand-browser-services/proxy.sock"); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("开发环境服务代理 Socket 未就绪")
}
