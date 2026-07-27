package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

const developmentWorkspacesRoot = containerRoot + "/workspaces"

var remoteUserInfo = regexp.MustCompile(`(?i)^(https?://)[^/@]+@`)

func (m *Manager) ScanRemoteProjects(ctx context.Context,
	manifest workerprotocol.EnvironmentManifest,
) ([]workerprotocol.DevelopmentProjectSnapshot, error) {
	if _, err := m.docker(ctx, "start", manifest.ContainerName); err != nil {
		return nil, err
	}
	owner := fmt.Sprintf("%d:%d", manifest.RuntimeUID, manifest.RuntimeGID)
	if _, err := m.docker(ctx, "exec", "--user", "0:0", manifest.ContainerName,
		"mkdir", "-p", developmentWorkspacesRoot); err != nil {
		return nil, err
	}
	if _, err := m.docker(ctx, "exec", "--user", "0:0", manifest.ContainerName,
		"chown", owner, developmentWorkspacesRoot); err != nil {
		return nil, err
	}
	output, err := m.docker(ctx, "exec", "--user", owner, "--env", "HOME="+manifest.RuntimeHome,
		manifest.ContainerName, "find", developmentWorkspacesRoot,
		"-mindepth", "1", "-maxdepth", "1", "-type", "d", "!", "-name", ".*",
		"-printf", "%f\x00")
	if err != nil {
		return nil, fmt.Errorf("扫描开发项目目录: %w", err)
	}
	names := strings.Split(output, "\x00")
	result := make([]workerprotocol.DevelopmentProjectSnapshot, 0, len(names))
	for _, name := range names {
		if name == "" || strings.HasPrefix(name, ".") || strings.Contains(name, "/") {
			continue
		}
		project, scanErr := m.scanRemoteProject(ctx, manifest, owner, name)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, project)
	}
	return result, nil
}

func (m *Manager) scanRemoteProject(ctx context.Context, manifest workerprotocol.EnvironmentManifest,
	owner, name string,
) (workerprotocol.DevelopmentProjectSnapshot, error) {
	relative := path.Join("workspaces", name)
	directory := path.Join(containerRoot, relative)
	result := workerprotocol.DevelopmentProjectSnapshot{
		Name: name, RelativePath: relative, ProjectKind: "directory",
	}
	gitMarker, err := m.remotePathExists(ctx, manifest.ContainerName, directory+"/.git")
	if err != nil {
		return result, err
	}
	if !gitMarker {
		return result, nil
	}
	base := []string{"exec", "--user", owner, "--env", "HOME=" + manifest.RuntimeHome,
		manifest.ContainerName, "git", "-C", directory}
	root, err := m.docker(ctx, append(base, "rev-parse", "--show-toplevel")...)
	if err != nil {
		return result, fmt.Errorf("读取项目 %q Git 根目录: %w", relative, err)
	}
	if strings.TrimSpace(root) != directory {
		return result, fmt.Errorf("项目 %q 的 Git 根目录不匹配", relative)
	}
	status, err := m.docker(ctx, append(base, "status", "--porcelain=v1",
		"--untracked-files=normal")...)
	if err != nil {
		return result, fmt.Errorf("读取项目 %q Git 状态: %w", relative, err)
	}
	result.ProjectKind = "git"
	result.Dirty = strings.TrimSpace(status) != ""
	result.Branch, _ = m.docker(ctx, append(base, "symbolic-ref", "--short", "-q", "HEAD")...)
	result.HeadSHA, _ = m.docker(ctx, append(base, "rev-parse", "--verify", "HEAD")...)
	remote, _ := m.docker(ctx, append(base, "remote", "get-url", "origin")...)
	result.Branch = strings.TrimSpace(result.Branch)
	result.HeadSHA = strings.TrimSpace(result.HeadSHA)
	result.RemoteURL = RedactGitRemote(remote)
	return result, nil
}

func RedactGitRemote(value string) string {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err == nil && parsed.Scheme != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		return parsed.String()
	}
	return remoteUserInfo.ReplaceAllString(value, "$1")
}

func (m *Manager) RelocateRemoteProject(ctx context.Context, operation RemoteOperation) error {
	unlock := LockRemoteEnvironment(operation.EnvironmentID)
	defer unlock()
	source, err := ContainerWorkspacePath(operation.Workspace)
	if err != nil {
		return err
	}
	target, err := ContainerWorkspacePath(operation.TargetWorkspace)
	if err != nil {
		return err
	}
	if source == target {
		return nil
	}
	sourceExists, err := m.remotePathExists(ctx, operation.ContainerName, source)
	if err != nil {
		return err
	}
	targetExists, err := m.remotePathExists(ctx, operation.ContainerName, target)
	if err != nil {
		return err
	}
	switch {
	case !sourceExists && targetExists:
		return nil
	case !sourceExists:
		return errors.New("待迁移项目目录不存在")
	case targetExists:
		return errors.New("项目迁移目标已存在，未覆盖任何目录")
	}
	if _, err := m.docker(ctx, "exec", "--user", "0:0", operation.ContainerName,
		"mkdir", "-p", path.Dir(target)); err != nil {
		return err
	}
	_, err = m.docker(ctx, "exec", "--user", "0:0", operation.ContainerName,
		"mv", "--", source, target)
	return err
}

func (m *Manager) remotePathExists(ctx context.Context, container, target string) (bool, error) {
	output, err := m.docker(ctx, "exec", "--user", "0:0", container, "/bin/sh", "-c",
		`if [ -e "$1" ]; then printf 1; else printf 0; fi`, "--", target)
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(output) {
	case "1":
		return true, nil
	case "0":
		return false, nil
	default:
		return false, errors.New("容器路径检测返回无效结果")
	}
}
