package hostworker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

type DependencyCheck struct {
	Name   string
	Path   string
	Status string
}

func Doctor(ctx context.Context, options RuntimeOptions, shell, authorizedKeys string) ([]DependencyCheck, error) {
	checks := make([]DependencyCheck, 0, 7)
	for _, name := range []string{"git", "ssh", "scp", "ssh-agent"} {
		path, err := exec.LookPath(name)
		if err != nil {
			return checks, fmt.Errorf("宿主机缺少必要工具 %s: %w", name, err)
		}
		checks = append(checks, DependencyCheck{Name: name, Path: path, Status: "ok"})
	}
	if shell == "" {
		return checks, fmt.Errorf("宿主 Shell 未配置")
	}
	if info, err := os.Stat(shell); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return checks, fmt.Errorf("宿主 Shell 不可执行: %s", shell)
	}
	checks = append(checks, DependencyCheck{Name: "shell", Path: shell, Status: "ok"})
	codexPath, err := exec.LookPath(options.CodexBin)
	if err != nil {
		return checks, fmt.Errorf("宿主机缺少 Codex CLI: %w", err)
	}
	versionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = codex.ValidateVersion(versionCtx, codexPath)
	cancel()
	if err != nil {
		return checks, err
	}
	checks = append(checks, DependencyCheck{Name: "codex", Path: codexPath,
		Status: ">=" + codex.RequiredVersion})
	if _, err := LoadAuthorizedClients(authorizedKeys); err != nil {
		return checks, err
	}
	checks = append(checks, DependencyCheck{Name: "authorized_keys", Path: authorizedKeys,
		Status: "ok"})
	for _, directory := range []string{options.Home, options.CodexHome, options.WorkspaceRoot,
		options.StateDir} {
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return checks, err
		}
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return checks, fmt.Errorf("宿主目录不可写 %s: %w", absolute, err)
		}
	}
	checks = append(checks, DependencyCheck{Name: "directories", Path: options.WorkspaceRoot,
		Status: "ok"})
	return checks, nil
}
