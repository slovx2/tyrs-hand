package devcontainer

import (
	"context"
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

const maxBrowserFileSize = 25 * 1024 * 1024

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
	if _, err := os.Stat(m.codexBin); err != nil {
		return fmt.Errorf("读取 Codex 原生程序: %w", err)
	}
	if _, err := m.docker(ctx, "exec", "--user", "0:0", container, "mkdir", "-p", runtimeRoot+"/bin"); err != nil {
		return err
	}
	if _, err := m.docker(ctx, "cp", "--follow-link", m.codexBin, container+":"+runtimeRoot+"/bin/codex"); err != nil {
		return err
	}
	if _, err := os.Stat(m.replyHook); err == nil {
		if _, err := m.docker(ctx, "cp", m.replyHook, container+":"+runtimeRoot+"/bin/tyrs-hand-reply-hook"); err != nil {
			return err
		}
	}
	owner := fmt.Sprintf("%d:%d", uid, gid)
	_, err := m.docker(ctx, "exec", "--user", "0:0", container, "/bin/sh", "-c",
		"chmod 0755 /opt/tyrs-hand/bin/* && ln -sf codex /opt/tyrs-hand/bin/apply_patch && chown -R "+owner+" /opt/tyrs-hand")
	if err != nil || !m.sshEnabled {
		return err
	}
	include := "Include " + filepath.ToSlash(filepath.Join(m.sshAgentDir, "ssh_config"))
	script := `set -eu
mkdir -p "$TYRS_HOME/.ssh"
config="$TYRS_HOME/.ssh/config"
if ! test -f "$config" || ! grep -Fqx "$TYRS_INCLUDE" "$config"; then
  temporary="$config.tyrs-hand.tmp"
  printf '%s\n' "$TYRS_INCLUDE" > "$temporary"
  test ! -f "$config" || cat "$config" >> "$temporary"
  mv "$temporary" "$config"
fi
chmod 0700 "$TYRS_HOME/.ssh"
chmod 0600 "$config"
chown -R "$TYRS_OWNER" "$TYRS_HOME/.ssh"`
	_, err = m.docker(ctx, "exec", "--user", "0:0", "--env", "TYRS_HOME="+home,
		"--env", "TYRS_INCLUDE="+include, "--env", "TYRS_OWNER="+owner,
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
		m.sweepIdle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) sweepIdle(ctx context.Context) {
	rows, err := m.db.QueryContext(ctx, `SELECT e.id, e.container_name
		FROM discord_development_environments e
		WHERE e.status = 'running' AND e.idle_at IS NOT NULL AND e.idle_at <= now()
		AND NOT EXISTS (
			SELECT 1 FROM discord_forums f JOIN discord_conversations c ON c.forum_id = f.id
			JOIN codex_turn_intents i ON i.discord_conversation_id = c.id
			WHERE f.development_environment_id = e.id
			AND i.status IN ('dispatching','awaiting_confirmation','running','reconciling'))`)
	if err != nil {
		m.logger.Warn("扫描空闲开发容器失败", zapError(err))
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, container string
		if rows.Scan(&id, &container) != nil {
			continue
		}
		if _, err := m.docker(ctx, "stop", "--time", "10", container); err != nil {
			m.logger.Warn("停止空闲开发容器失败", zapString("container", container), zapError(err))
			continue
		}
		_, _ = m.db.ExecContext(ctx, `UPDATE discord_development_environments
			SET status = 'stopped', idle_at = NULL, updated_at = now() WHERE id = $1`, id)
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
