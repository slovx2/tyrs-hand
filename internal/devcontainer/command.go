package devcontainer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type commandRunner interface {
	Run(context.Context, []string, string, ...string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, environment []string, directory string, arguments ...string) (string, error) {
	if len(arguments) == 0 {
		return "", fmt.Errorf("命令不能为空")
	}
	command := exec.CommandContext(ctx, arguments[0], arguments[1:]...)
	command.Dir = directory
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return text, fmt.Errorf("执行 %s: %w: %s", arguments[0], err, text)
	}
	return text, nil
}
