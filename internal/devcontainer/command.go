package devcontainer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type commandRunner interface {
	Run(context.Context, []string, string, ...string) (string, error)
}

type commandStreamRunner interface {
	Open(context.Context, []string, string, ...string) (io.ReadCloser, error)
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

func (execRunner) Open(ctx context.Context, environment []string, directory string,
	arguments ...string,
) (io.ReadCloser, error) {
	if len(arguments) == 0 {
		return nil, fmt.Errorf("命令不能为空")
	}
	command := exec.CommandContext(ctx, arguments[0], arguments[1:]...)
	command.Dir = directory
	command.Env = append(os.Environ(), environment...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		return nil, err
	}
	return &commandReadCloser{stdout: stdout, command: command, stderr: &stderr}, nil
}

type commandReadCloser struct {
	stdout  io.ReadCloser
	command *exec.Cmd
	stderr  *bytes.Buffer
	once    sync.Once
	err     error
}

func (r *commandReadCloser) Read(value []byte) (int, error) { return r.stdout.Read(value) }

func (r *commandReadCloser) Close() error {
	r.once.Do(func() {
		_ = r.stdout.Close()
		r.err = r.command.Wait()
		if r.err != nil {
			detail := strings.TrimSpace(r.stderr.String())
			if detail != "" {
				r.err = fmt.Errorf("流式命令失败: %w: %s", r.err, detail)
			}
		}
	})
	return r.err
}
