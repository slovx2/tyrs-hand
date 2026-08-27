package hostworker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	_ "modernc.org/sqlite"
)

var remoteUserInfo = regexp.MustCompile(`(?i)^(https?://)[^/@]+@`)

// ScanProjects 发现 Workspace 根目录、一级子目录以及 Codex 注册项目。
// codexHome 为可选参数，省略时仅扫描 Workspace 项目（兼容旧调用方）。
func ScanProjects(ctx context.Context, root string, codexHome ...string) ([]workerprotocol.WorkspaceProjectSnapshot, error) {
	root = filepath.Clean(root)
	projects := []workerprotocol.WorkspaceProjectSnapshot{scanDirectoryProject(ctx, root, "Workspace", "workspaces", "workspace_root")}
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return projects, fmt.Errorf("扫描宿主工作区: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || name == "" || strings.HasPrefix(name, ".") || strings.Contains(name, "/") {
			continue
		}
		projects = append(projects, scanDirectoryProject(ctx, filepath.Join(root, name), name, path.Join("workspaces", name), "workspace_child"))
	}
	if len(codexHome) == 0 || strings.TrimSpace(codexHome[0]) == "" {
		return projects, nil
	}
	registered := discoverCodexProjects(codexHome[0])
	seen := map[string]bool{}
	for _, p := range projects {
		if canonical, e := filepath.EvalSymlinks(p.HostPath); e == nil {
			seen[filepath.Clean(canonical)] = true
		}
	}
	for _, hostPath := range registered {
		canonical := hostPath
		if p, e := filepath.EvalSymlinks(hostPath); e == nil {
			canonical = filepath.Clean(p)
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		hash := sha256.Sum256([]byte(canonical))
		name := filepath.Base(canonical)
		if name == "." || name == string(filepath.Separator) || name == "" {
			name = canonical
		}
		project := workerprotocol.WorkspaceProjectSnapshot{Name: name, RelativePath: "codex/" + hex.EncodeToString(hash[:]), ProjectSource: "codex_registered", HostPath: canonical, ProjectKind: "directory"}
		if info, e := os.Stat(canonical); e == nil && info.IsDir() {
			project = scanDirectoryProject(ctx, canonical, name, project.RelativePath, "codex_registered")
			project.HostPath, project.ProjectSource = canonical, "codex_registered"
		} else {
			project.ScanError = "Codex 注册项目不可访问"
		}
		projects = append(projects, project)
	}
	sort.SliceStable(projects, func(i, j int) bool {
		rank := func(s string) int {
			if s == "workspace_root" {
				return 0
			}
			if s == "workspace_child" {
				return 1
			}
			return 2
		}
		if rank(projects[i].ProjectSource) != rank(projects[j].ProjectSource) {
			return rank(projects[i].ProjectSource) < rank(projects[j].ProjectSource)
		}
		return strings.ToLower(projects[i].Name) < strings.ToLower(projects[j].Name)
	})
	return projects, nil
}

func scanDirectoryProject(ctx context.Context, directory, name, relative, source string) workerprotocol.WorkspaceProjectSnapshot {
	result := workerprotocol.WorkspaceProjectSnapshot{Name: name, RelativePath: relative, ProjectSource: source, HostPath: filepath.Clean(directory), ProjectKind: "directory"}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		result.ScanError = "项目目录不存在或不可访问"
		return result
	}
	result.Available = true
	if _, err := os.Stat(filepath.Join(directory, ".git")); err != nil {
		if !os.IsNotExist(err) {
			result.ScanError = err.Error()
		}
		return result
	}
	rootOutput, err := gitOutput(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		result.ScanError = "读取 Git 根目录失败"
		return result
	}
	if filepath.Clean(rootOutput) != filepath.Clean(directory) {
		result.ScanError = "Git 根目录不匹配"
		return result
	}
	status, err := gitOutput(ctx, directory, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		result.ScanError = "读取 Git 状态失败"
		return result
	}
	result.ProjectKind, result.Dirty = "git", status != ""
	result.Branch, _ = gitOutput(ctx, directory, "symbolic-ref", "--short", "-q", "HEAD")
	result.HeadSHA, _ = gitOutput(ctx, directory, "rev-parse", "--verify", "HEAD")
	remote, _ := gitOutput(ctx, directory, "remote", "get-url", "origin")
	result.RemoteURL = redactGitRemote(remote)
	return result
}

type codexGlobalState struct {
	ActiveWorkspaceRoots        []string                   `json:"active-workspace-roots"`
	ElectronSavedWorkspaceRoots []string                   `json:"electron-saved-workspace-roots"`
	ElectronWorkspaceRootLabels map[string]json.RawMessage `json:"electron-workspace-root-labels"`
	ThreadWorkspaceRootHints    map[string]json.RawMessage `json:"thread-workspace-root-hints"`
}

func discoverCodexProjects(codexHome string) []string {
	seen := map[string]bool{}
	result := []string{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || !filepath.IsAbs(value) {
			return
		}
		value = filepath.Clean(value)
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	var state codexGlobalState
	if data, err := os.ReadFile(filepath.Join(codexHome, ".codex-global-state.json")); err == nil && json.Unmarshal(data, &state) == nil {
		for _, p := range state.ActiveWorkspaceRoots {
			add(p)
		}
		for _, p := range state.ElectronSavedWorkspaceRoots {
			add(p)
		}
		for p := range state.ElectronWorkspaceRootLabels {
			add(p)
		}
		for _, raw := range state.ThreadWorkspaceRootHints {
			addRawRegisteredPaths(raw, add)
		}
	}
	// 使用纯 Go SQLite 驱动读取状态库，保持 Worker 构建 CGO_ENABLED=0。
	paths := []string{os.Getenv("CODEX_SQLITE_HOME"), codexHome, filepath.Join(codexHome, "sqlite")}
	files := []string{"state_5.sqlite", "state.v2.sqlite", "state.sqlite", "codex.sqlite"}
	for _, dir := range paths {
		if dir == "" {
			continue
		}
		for _, file := range files {
			db := filepath.Join(dir, file)
			if _, err := os.Stat(db); err != nil {
				continue
			}
			database, err := sql.Open("sqlite", db)
			if err != nil {
				continue
			}
			rows, err := database.Query("SELECT DISTINCT cwd FROM threads WHERE cwd IS NOT NULL AND TRIM(cwd) != ''")
			if err == nil {
				for rows.Next() {
					var cwd string
					if rows.Scan(&cwd) == nil {
						add(cwd)
					}
				}
				_ = rows.Close()
			}
			_ = database.Close()
		}
	}
	sort.Strings(result)
	return result
}

func addRawRegisteredPaths(raw json.RawMessage, add func(string)) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return
	}
	var walk func(any)
	walk = func(item any) {
		switch typed := item.(type) {
		case string:
			add(typed)
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case map[string]any:
			for key, child := range typed {
				if key == "cwd" || key == "path" || key == "root" || key == "workspaceRoot" {
					walk(child)
				}
			}
		}
	}
	walk(value)
}

// CodexProjectRegistered 校验路径仍存在于 Codex 当前注册记录中。
func CodexProjectRegistered(codexHome, projectPath string) bool {
	canonical := filepath.Clean(projectPath)
	if resolved, err := filepath.EvalSymlinks(canonical); err == nil {
		canonical = filepath.Clean(resolved)
	}
	for _, registered := range discoverCodexProjects(codexHome) {
		candidate := filepath.Clean(registered)
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			candidate = filepath.Clean(resolved)
		}
		if candidate == canonical {
			return true
		}
	}
	return false
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
