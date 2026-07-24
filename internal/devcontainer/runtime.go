package devcontainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"go.uber.org/zap"
)

const (
	maxBrowserFileSize   = 25 * 1024 * 1024
	runtimeSignaturePath = runtimeRoot + "/.codex-runtime-signature"
	managedSSHConfigPath = "/etc/ssh/ssh_config.d/99-tyrs-hand.conf"
	sshAgentProfilePath  = "/etc/profile.d/tyrs-hand-ssh-agent.sh"
)

func (m *Manager) desiredRuntimeSignature() (string, error) {
	m.runtimeSignatureMu.Lock()
	defer m.runtimeSignatureMu.Unlock()
	if m.runtimeSignature != "" {
		return m.runtimeSignature, nil
	}
	hash := sha256.New()
	for _, path := range []string{m.codexBin, m.codexProxyBin, m.replyHook} {
		file, err := os.Open(path)
		if err != nil {
			if path == m.replyHook && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		_, _ = hash.Write([]byte{0})
	}
	for _, argument := range codex.ManagedAppServerArguments("unix://" + appServerSocket) {
		_, _ = hash.Write([]byte(argument))
		_, _ = hash.Write([]byte{0})
	}
	m.runtimeSignature = hex.EncodeToString(hash.Sum(nil))
	return m.runtimeSignature, nil
}

// RefreshRemoteRuntime 在 Worker 镜像变化后更新长期运行的环境。
func (m *Manager) RefreshRemoteRuntime(ctx context.Context, runtime Runtime) (bool, error) {
	if m.sshEnabled {
		owner := fmt.Sprintf("%d:%d", runtime.UID, runtime.GID)
		if err := m.installSSHConfiguration(ctx, runtime.Container, runtime.Home, owner); err != nil {
			return false, fmt.Errorf("刷新 SSH 配置: %w", err)
		}
	}
	desired, err := m.desiredRuntimeSignature()
	if err != nil {
		return false, fmt.Errorf("计算 Codex 运行时签名: %w", err)
	}
	current, _ := m.docker(ctx, "exec", runtime.Container, "cat", runtimeSignaturePath)
	if strings.TrimSpace(current) == desired {
		return false, nil
	}
	if err := m.StopRemoteAppServer(ctx, runtime.Container); err != nil {
		return false, fmt.Errorf("停止旧 Codex app-server: %w", err)
	}
	if err := m.installRuntime(ctx, runtime.Container, runtime.UID, runtime.GID,
		runtime.Home); err != nil {
		return false, fmt.Errorf("刷新 Codex 运行时: %w", err)
	}
	return true, nil
}

func createAskPass(credential string) (string, func(), error) {
	directory, err := os.MkdirTemp("", "tyrs-hand-git-askpass-*")
	if err != nil {
		return "", func() {}, err
	}
	path := filepath.Join(directory, "askpass.sh")
	script := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' x-access-token ;;\n*) printf '%s\\n' \"$TYRS_GIT_TOKEN\" ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		_ = os.RemoveAll(directory)
		return "", func() {}, err
	}
	return path, func() { _ = os.RemoveAll(directory) }, nil
}

func (m *Manager) ensureDockerResource(ctx context.Context, kind, name string) error {
	if _, err := m.docker(ctx, kind, "inspect", name); err == nil {
		return nil
	}
	arguments := []string{kind, "create", "--label", "com.tyrs-hand.managed=true"}
	if kind == "network" {
		arguments = append(arguments, "--driver", "bridge")
	}
	arguments = append(arguments, name)
	_, err := m.docker(ctx, arguments...)
	return err
}

func (m *Manager) installRuntime(ctx context.Context, container string, uid, gid int64,
	home string,
) error {
	signature, err := m.desiredRuntimeSignature()
	if err != nil {
		return fmt.Errorf("计算 Codex 运行时签名: %w", err)
	}
	if _, err := os.Stat(m.codexBin); err != nil {
		return fmt.Errorf("读取 Codex 原生程序: %w", err)
	}
	if _, err := os.Stat(m.codexProxyBin); err != nil {
		return fmt.Errorf("读取 Codex Desktop Proxy: %w", err)
	}
	if _, err := m.docker(ctx, "exec", "--user", "0:0", container, "mkdir", "-p",
		runtimeRoot+"/bin", runtimeRoot+"/libexec"); err != nil {
		return err
	}
	if _, err := m.docker(ctx, "cp", "--follow-link", m.codexBin,
		container+":"+runtimeRoot+"/libexec/codex-real"); err != nil {
		return err
	}
	if _, err := m.docker(ctx, "cp", m.codexProxyBin, container+":"+runtimeRoot+"/bin/codex"); err != nil {
		return err
	}
	if _, err := os.Stat(m.replyHook); err == nil {
		if _, err := m.docker(ctx, "cp", m.replyHook, container+":"+runtimeRoot+"/bin/tyrs-hand-reply-hook"); err != nil {
			return err
		}
	}
	owner := fmt.Sprintf("%d:%d", uid, gid)
	_, err = m.docker(ctx, "exec", "--user", "0:0",
		"--env", "TYRS_RUNTIME_SIGNATURE="+signature, container, "/bin/sh", "-c",
		"chmod 0755 /opt/tyrs-hand/bin/* /opt/tyrs-hand/libexec/* && "+
			"ln -sf ../libexec/codex-real /opt/tyrs-hand/bin/apply_patch && "+
			"ln -sf /opt/tyrs-hand/bin/codex /usr/local/bin/codex && "+
			"printf '%s' \"$TYRS_RUNTIME_SIGNATURE\" > "+runtimeSignaturePath+" && "+
			"chown -R "+owner+" /opt/tyrs-hand")
	if err != nil || !m.sshEnabled {
		return err
	}
	return m.installSSHConfiguration(ctx, container, home, owner)
}

func (m *Manager) installSSHConfiguration(ctx context.Context, container, home, owner string) error {
	source := filepath.Join(m.sshAgentDir, "ssh_config")
	content, err := os.ReadFile(source)
	sourceExists := err == nil
	if errors.Is(err, os.ErrNotExist) {
		content = []byte("Host *\n")
	} else if err != nil {
		return err
	}
	checksum := sha256.Sum256(content)
	expectedChecksum := hex.EncodeToString(checksum[:])
	currentChecksum, _ := m.docker(ctx, "exec", container, "cat", managedSSHConfigPath+".sha256")
	if strings.TrimSpace(currentChecksum) == expectedChecksum {
		return nil
	}
	if _, err := m.docker(ctx, "exec", "--user", "0:0", container, "mkdir", "-p",
		filepath.Dir(managedSSHConfigPath), filepath.Dir(sshAgentProfilePath)); err != nil {
		return err
	}
	temporaryConfig := managedSSHConfigPath + ".tmp"
	if sourceExists {
		if _, err := m.docker(ctx, "cp", source, container+":"+temporaryConfig); err != nil {
			return err
		}
	} else if _, err := m.docker(ctx, "exec", "--user", "0:0", container,
		"/bin/sh", "-c", "printf 'Host *\\n' > "+temporaryConfig); err != nil {
		return err
	}
	include := "Include " + managedSSHConfigPath
	legacyInclude := "Include " + filepath.ToSlash(filepath.Join(m.sshAgentDir, "ssh_config"))
	script := `set -eu
mkdir -p "$TYRS_HOME/.ssh"
system_config="/etc/ssh/ssh_config"
system_temporary="$system_config.tyrs-hand.tmp"
printf '%s\n' "$TYRS_INCLUDE" > "$system_temporary"
if test -f "$system_config"; then
  while IFS= read -r line || test -n "$line"; do
    if test "$line" != "$TYRS_INCLUDE" && test "$line" != "$TYRS_LEGACY_INCLUDE"; then
      printf '%s\n' "$line"
    fi
  done < "$system_config" >> "$system_temporary"
fi
mv "$system_temporary" "$system_config"
chmod 0644 "$system_config"
chown 0:0 "$system_config"
chown 0:0 "$TYRS_MANAGED_CONFIG"
chmod 0644 "$TYRS_MANAGED_CONFIG"
mv "$TYRS_MANAGED_CONFIG" "$TYRS_CONFIG"
printf 'export SSH_AUTH_SOCK=%s\n' "$TYRS_AGENT_SOCKET" > "$TYRS_PROFILE"
chmod 0644 "$TYRS_PROFILE"
chown 0:0 "$TYRS_PROFILE"
config="$TYRS_HOME/.ssh/config"
if test -f "$config"; then
  temporary="$config.tyrs-hand.tmp"
  : > "$temporary"
  while IFS= read -r line || test -n "$line"; do
    if test "$line" != "$TYRS_INCLUDE" && test "$line" != "$TYRS_LEGACY_INCLUDE"; then
      printf '%s\n' "$line"
    fi
  done < "$config" >> "$temporary"
  mv "$temporary" "$config"
  chmod 0600 "$config"
fi
chmod 0700 "$TYRS_HOME/.ssh"
chown -R "$TYRS_OWNER" "$TYRS_HOME/.ssh"
printf '%s\n' "$TYRS_CHECKSUM" > "$TYRS_CONFIG.sha256"
chmod 0644 "$TYRS_CONFIG.sha256"
chown 0:0 "$TYRS_CONFIG.sha256"`
	_, err = m.docker(ctx, "exec", "--user", "0:0", "--env", "TYRS_HOME="+home,
		"--env", "TYRS_INCLUDE="+include, "--env", "TYRS_LEGACY_INCLUDE="+legacyInclude,
		"--env", "TYRS_OWNER="+owner, "--env", "TYRS_MANAGED_CONFIG="+temporaryConfig,
		"--env", "TYRS_CONFIG="+managedSSHConfigPath, "--env", "TYRS_CHECKSUM="+expectedChecksum,
		"--env", "TYRS_AGENT_SOCKET="+filepath.Join(m.sshAgentDir, "current.sock"),
		"--env", "TYRS_PROFILE="+sshAgentProfilePath,
		container, "/bin/sh", "-c", script)
	return err
}

func (m *Manager) CopyToContainer(ctx context.Context, container, source, target string) error {
	if _, err := m.docker(ctx, "exec", "--user", "0:0", container, "mkdir", "-p", target); err != nil {
		return err
	}
	_, err := m.docker(ctx, "cp", filepath.Clean(source)+"/.", container+":"+target)
	return err
}

func (m *Manager) CopyToRuntime(ctx context.Context, runtime Runtime, source, target string) error {
	if err := m.CopyToContainer(ctx, runtime.Container, source, target); err != nil {
		return err
	}
	_, err := m.docker(ctx, "exec", "--user", "0:0", runtime.Container, "chown", "-R",
		fmt.Sprintf("%d:%d", runtime.UID, runtime.GID), target)
	return err
}

func (m *Manager) ContainerIP(ctx context.Context, runtime Runtime) (string, error) {
	value, err := m.docker(ctx, "inspect", "--format",
		"{{range .NetworkSettings.Networks}}{{println .IPAddress}}{{end}}", runtime.Container)
	if err != nil {
		return "", err
	}
	for _, address := range strings.Fields(value) {
		if address != "" {
			return address, nil
		}
	}
	return "", errors.New("开发容器没有可用的 IPv4 地址")
}

func (m *Manager) ExportWorkspaceFile(ctx context.Context, runtime Runtime,
	source, target string,
) error {
	clean := filepath.ToSlash(filepath.Clean(source))
	workspace := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(runtime.Workspace)), "/")
	if clean != workspace && !strings.HasPrefix(clean, workspace+"/") {
		return errors.New("文件不在当前工作区内")
	}
	resolved, err := m.docker(ctx, "exec", runtime.Container, "realpath", "-e", "--", clean)
	if err != nil || strings.TrimSpace(resolved) != clean {
		return errors.New("文件路径不存在或包含符号链接")
	}
	metadata, err := m.docker(ctx, "exec", runtime.Container, "stat", "-c", "%F:%s", "--", clean)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimSpace(metadata), ":")
	if len(parts) != 2 || parts[0] != "regular file" {
		return errors.New("只能交换普通文件")
	}
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || size > maxBrowserFileSize {
		return errors.New("文件大小超过 25 MiB")
	}
	_, err = m.docker(ctx, "cp", runtime.Container+":"+clean, target)
	return err
}

func (m *Manager) ImportWorkspaceFile(ctx context.Context, runtime Runtime,
	source, destination string,
) error {
	clean := filepath.ToSlash(filepath.Clean(destination))
	workspace := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(runtime.Workspace)), "/")
	if clean == workspace || !strings.HasPrefix(clean, workspace+"/") {
		return errors.New("目标文件不在当前工作区内")
	}
	parent := filepath.ToSlash(filepath.Dir(clean))
	resolved, err := m.docker(ctx, "exec", runtime.Container, "realpath", "-m", "--", parent)
	if err != nil || strings.TrimSpace(resolved) != parent {
		return errors.New("目标目录包含符号链接")
	}
	if _, err := m.docker(ctx, "exec", "--user", fmt.Sprintf("%d:%d", runtime.UID, runtime.GID),
		runtime.Container, "mkdir", "-p", "--", parent); err != nil {
		return err
	}
	if _, err := m.docker(ctx, "cp", source, runtime.Container+":"+clean); err != nil {
		return err
	}
	_, err = m.docker(ctx, "exec", "--user", "0:0", runtime.Container, "chown",
		fmt.Sprintf("%d:%d", runtime.UID, runtime.GID), clean)
	return err
}

func (m *Manager) Launcher(runtime Runtime) codex.Launcher {
	return dockerLauncher{dockerBin: m.dockerBin, container: runtime.Container,
		user: fmt.Sprintf("%d:%d", runtime.UID, runtime.GID), home: runtime.Home, dockerHost: m.dockerHost}
}

func (m *Manager) RunSweeper(ctx context.Context) {
	if !m.Enabled() {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		m.processOperation(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type dockerLauncher struct {
	dockerBin  string
	container  string
	user       string
	home       string
	dockerHost string
}

func (l dockerLauncher) Launch(spec codex.ProcessSpec) (codex.Process, error) {
	arguments := []string{"exec", "--interactive", "--user", l.user, "--env", "HOME=" + l.home,
		"--workdir", spec.Dir}
	for _, value := range spec.Env {
		arguments = append(arguments, "--env", value)
	}
	arguments = append(arguments, l.container, spec.Bin)
	arguments = append(arguments, spec.Args...)
	command := exec.Command(l.dockerBin, arguments...)
	command.Env = os.Environ()
	if l.dockerHost != "inherit" {
		command.Env = append(command.Env, "DOCKER_HOST="+l.dockerHost)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &dockerProcess{command: command, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

type dockerProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
}

func (p *dockerProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *dockerProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *dockerProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *dockerProcess) Wait() error           { return p.command.Wait() }
func (p *dockerProcess) Signal(signal os.Signal) error {
	if p.command.Process == nil {
		return os.ErrProcessDone
	}
	return p.command.Process.Signal(signal)
}
func (p *dockerProcess) Kill() error {
	if p.command.Process == nil {
		return os.ErrProcessDone
	}
	return p.command.Process.Kill()
}

// 小包装避免让日志辅助类型渗入容器核心逻辑。
func zapError(err error) zap.Field          { return zap.Error(err) }
func zapString(key, value string) zap.Field { return zap.String(key, value) }
