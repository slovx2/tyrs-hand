package devenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

type commandRunner interface {
	Run(context.Context, string, []string, string, ...string) (string, error)
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, dir string, environment []string, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.Dir = dir
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if len(text) > 4000 {
			text = text[len(text)-4000:]
		}
		if text != "" {
			return text, fmt.Errorf("%s: %w: %s", name, err, text)
		}
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return strings.TrimSpace(string(output)), nil
}
