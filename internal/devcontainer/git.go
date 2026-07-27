package devcontainer

import (
	"context"
	"fmt"
	"strings"
)

func (m *Manager) Git(ctx context.Context, runtime Runtime, arguments ...string) (string, error) {
	base := []string{"exec", "--user", fmt.Sprintf("%d:%d", runtime.UID, runtime.GID),
		"--env", "HOME=" + runtime.Home, "--workdir", runtime.Workspace, runtime.Container, "git"}
	return m.docker(ctx, append(base, arguments...)...)
}

func (m *Manager) Commit(ctx context.Context, runtime Runtime, message string) (string, error) {
	status, err := m.Git(ctx, runtime, "status", "--porcelain=v1")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) == "" {
		return m.Git(ctx, runtime, "rev-parse", "HEAD")
	}
	if _, err := m.Git(ctx, runtime, "add", "--all"); err != nil {
		return "", err
	}
	if _, err := m.Git(ctx, runtime, "-c", "user.name=TyrsHand Agent",
		"-c", "user.email=tyrs-hand[bot]@users.noreply.github.com", "commit", "-m", message); err != nil {
		return "", err
	}
	return m.Git(ctx, runtime, "rev-parse", "HEAD")
}

func (m *Manager) Publish(ctx context.Context, runtime Runtime) (string, string, error) {
	branch, err := m.Git(ctx, runtime, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", "", err
	}
	arguments := []string{"exec", "--env", "HOME=" + runtime.Home,
		"--user", fmt.Sprintf("%d:%d", runtime.UID, runtime.GID),
		"--workdir", runtime.Workspace, runtime.Container, "git", "push", "origin",
		"HEAD:refs/heads/" + strings.TrimSpace(branch)}
	if _, err := m.docker(ctx, arguments...); err != nil {
		return "", "", err
	}
	sha, err := m.Git(ctx, runtime, "rev-parse", "HEAD")
	return strings.TrimSpace(branch), strings.TrimSpace(sha), err
}
