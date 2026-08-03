package hostworker

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

var remoteUserInfo = regexp.MustCompile(`(?i)^(https?://)[^/@]+@`)

// ScanProjects 只读取宿主固定工作区下的一级目录，不修改仓库和用户文件。
func ScanProjects(ctx context.Context, root string) ([]workerprotocol.DevelopmentProjectSnapshot, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("扫描宿主工作区: %w", err)
	}
	projects := make([]workerprotocol.DevelopmentProjectSnapshot, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || name == "" || strings.HasPrefix(name, ".") ||
			strings.Contains(name, "/") {
			continue
		}
		project, err := scanProject(ctx, root, name)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, nil
}

func scanProject(ctx context.Context, root, name string) (workerprotocol.DevelopmentProjectSnapshot, error) {
	directory := filepath.Join(root, name)
	result := workerprotocol.DevelopmentProjectSnapshot{
		Name: name, RelativePath: path.Join("workspaces", name), ProjectKind: "directory",
	}
	if _, err := os.Stat(filepath.Join(directory, ".git")); err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, err
	}
	rootOutput, err := gitOutput(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		return result, fmt.Errorf("读取项目 %q Git 根目录: %w", name, err)
	}
	if filepath.Clean(rootOutput) != filepath.Clean(directory) {
		return result, fmt.Errorf("项目 %q 的 Git 根目录不匹配", name)
	}
	status, err := gitOutput(ctx, directory, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return result, fmt.Errorf("读取项目 %q Git 状态: %w", name, err)
	}
	result.ProjectKind = "git"
	result.Dirty = status != ""
	result.Branch, _ = gitOutput(ctx, directory, "symbolic-ref", "--short", "-q", "HEAD")
	result.HeadSHA, _ = gitOutput(ctx, directory, "rev-parse", "--verify", "HEAD")
	remote, _ := gitOutput(ctx, directory, "remote", "get-url", "origin")
	result.RemoteURL = redactGitRemote(remote)
	return result, nil
}

func gitOutput(ctx context.Context, directory string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func redactGitRemote(value string) string {
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
