package devcontainer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

func (m *Manager) Publish(ctx context.Context, runtime Runtime, credential string) (string, string, error) {
	branch, err := m.Git(ctx, runtime, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", "", err
	}
	directory, err := os.MkdirTemp("", "tyrs-hand-container-git-*")
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	script := filepath.Join(directory, "askpass.sh")
	containerAskPass := "/tmp/tyrs-hand-askpass-" + filepath.Base(directory)
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' x-access-token ;;\n*) printf '%s\\n' \"$TYRS_GIT_TOKEN\" ;;\nesac\n"), 0o700); err != nil {
		return "", "", err
	}
	envFile := filepath.Join(directory, "git.env")
	if err := os.WriteFile(envFile, []byte("GIT_ASKPASS="+containerAskPass+"\nGIT_TERMINAL_PROMPT=0\nTYRS_GIT_TOKEN="+credential+"\n"), 0o600); err != nil {
		return "", "", err
	}
	if _, err := m.docker(ctx, "cp", script, runtime.Container+":"+containerAskPass); err != nil {
		return "", "", err
	}
	defer func() {
		_, _ = m.docker(context.Background(), "exec", "--user", "0:0", runtime.Container, "rm", "-f", containerAskPass)
	}()
	if _, err := m.docker(ctx, "exec", "--user", "0:0", runtime.Container, "chown",
		fmt.Sprintf("%d:%d", runtime.UID, runtime.GID), containerAskPass); err != nil {
		return "", "", err
	}
	arguments := []string{"exec", "--env-file", envFile, "--env", "HOME=" + runtime.Home,
		"--user", fmt.Sprintf("%d:%d", runtime.UID, runtime.GID),
		"--workdir", runtime.Workspace, runtime.Container, "git", "push", "origin",
		"HEAD:refs/heads/" + strings.TrimSpace(branch)}
	if _, err := m.docker(ctx, arguments...); err != nil {
		return "", "", err
	}
	sha, err := m.Git(ctx, runtime, "rev-parse", "HEAD")
	return strings.TrimSpace(branch), strings.TrimSpace(sha), err
}
