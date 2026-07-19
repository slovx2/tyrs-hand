package devenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"go.uber.org/zap"
)

type Manager struct {
	dataRoot string
	lock     RuntimeLock
	runner   commandRunner
	logger   *zap.Logger
}

type stateMarker struct {
	Status                string `json:"status"`
	RuntimeFingerprint    string `json:"runtimeFingerprint"`
	DependencyFingerprint string `json:"dependencyFingerprint"`
}

func NewManager(dataRoot string, logger *zap.Logger) (*Manager, error) {
	lock, err := LoadRuntimeLock()
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	manager := &Manager{dataRoot: dataRoot, lock: lock, runner: osCommandRunner{}, logger: logger}
	if err := manager.ensureDirectories(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) Prepare(ctx context.Context, workspace string) Result {
	spec, err := Resolve(workspace, m.lock)
	if err != nil {
		return degraded("resolve", err)
	}
	runtimeHash, err := runtimeFingerprint(spec, m.lock)
	if err != nil {
		return degraded("fingerprint", err)
	}
	dependencyHash, err := dependencyFingerprint(workspace, spec)
	if err != nil {
		return degraded("fingerprint", err)
	}
	result := Result{Status: "ready", RuntimeFingerprint: runtimeHash, DependencyFingerprint: dependencyHash}
	unlock, err := acquireFileLock(ctx, m.workspaceLockPath(workspace))
	if err != nil {
		return degradedWithFingerprints("lock", err, runtimeHash, dependencyHash)
	}
	defer unlock()
	previousMarker := m.loadMarker(workspace)
	runtimeChanged := previousMarker.RuntimeFingerprint != "" && previousMarker.RuntimeFingerprint != runtimeHash

	if m.ready(ctx, workspace, spec, runtimeHash, dependencyHash) {
		result.Projects = readyProjectResults(spec)
		result.Environment, err = m.environment(ctx, workspace, spec, nil)
		if err == nil {
			now := time.Now().UTC()
			result.PreparedAt = &now
			return result
		}
	}

	toolPaths, toolErrors := m.ensureToolchains(ctx, spec)
	for tool, toolErr := range toolErrors {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{
			Stage: "toolchain", Manager: tool, Message: toolErr.Error(),
			Hint: "请检查精确版本、网络和 Worker Toolchain Store",
		})
	}
	failedDependencies := make(map[string]bool)
	for _, project := range spec.Projects {
		projectResult := ProjectResult{Name: project.Name, Path: project.Path, Status: "ready"}
		for _, dependency := range project.Dependencies {
			dependencyResult := DependencyResult{Manager: dependency.Manager, Status: "ready"}
			if missingTool(dependency.Manager, toolErrors) {
				dependencyResult.Status = "degraded"
				projectResult.Status = "degraded"
				failedDependencies[dependencyKey(project, dependency)] = true
				projectResult.Dependencies = append(projectResult.Dependencies, dependencyResult)
				continue
			}
			if err := m.prepareDependency(ctx, workspace, spec, project, dependency, toolPaths, runtimeChanged); err != nil {
				dependencyResult.Status = "degraded"
				projectResult.Status = "degraded"
				failedDependencies[dependencyKey(project, dependency)] = true
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					Stage: "dependencies", Project: project.Name, Manager: dependency.Manager,
					Message: err.Error(), Hint: "只有任务需要运行或调试项目时才需要处理该问题",
				})
			}
			projectResult.Dependencies = append(projectResult.Dependencies, dependencyResult)
		}
		result.Projects = append(result.Projects, projectResult)
	}
	if len(result.Diagnostics) > 0 {
		result.Status = "degraded"
	} else {
		now := time.Now().UTC()
		result.PreparedAt = &now
	}
	result.Environment, err = m.environment(ctx, workspace, spec, failedDependencies)
	if err != nil {
		result.Status = "degraded"
		result.Diagnostics = append(result.Diagnostics, Diagnostic{Stage: "environment", Message: err.Error()})
	}
	if err := m.saveMarker(workspace, stateMarker{
		Status: result.Status, RuntimeFingerprint: runtimeHash, DependencyFingerprint: dependencyHash,
	}); err != nil {
		result.Status = "degraded"
		result.Diagnostics = append(result.Diagnostics, Diagnostic{Stage: "state", Message: err.Error()})
	}
	return result
}

func dependencyKey(project Project, dependency Dependency) string {
	return project.Path + "\x00" + dependency.Manager
}

func readyProjectResults(spec Spec) []ProjectResult {
	results := make([]ProjectResult, 0, len(spec.Projects))
	for _, project := range spec.Projects {
		result := ProjectResult{Name: project.Name, Path: project.Path, Status: "ready"}
		for _, dependency := range project.Dependencies {
			result.Dependencies = append(result.Dependencies, DependencyResult{Manager: dependency.Manager, Status: "ready"})
		}
		results = append(results, result)
	}
	return results
}

func (m *Manager) ensureToolchains(ctx context.Context, spec Spec) (map[string]string, map[string]error) {
	versions := map[string]string{
		"python": spec.Runtimes.Python, "node": spec.Runtimes.Node, "go": spec.Runtimes.Go,
	}
	paths := make(map[string]string)
	failures := make(map[string]error)
	for _, tool := range []string{"python", "node", "go"} {
		version := versions[tool]
		if version == "" {
			continue
		}
		path, err := m.ensureToolchain(ctx, tool, version)
		if err != nil {
			failures[tool] = err
		} else {
			paths[tool] = path
		}
	}
	if spec.Runtimes.Rust != nil {
		path, err := m.ensureRustToolchain(ctx, *spec.Runtimes.Rust)
		if err != nil {
			failures["rust"] = err
		} else {
			paths["rust"] = path
		}
	}
	if spec.Runtimes.PNPM != "" {
		if _, nodeFailed := failures["node"]; !nodeFailed {
			path, err := m.ensurePNPM(ctx, spec.Runtimes.Node, spec.Runtimes.PNPM)
			if err != nil {
				failures["pnpm"] = err
			} else {
				paths["pnpm"] = path
			}
		}
	}
	return paths, failures
}

func (m *Manager) ensureToolchain(ctx context.Context, tool, version string) (string, error) {
	key := tool + "-" + version + "-" + m.platformKey()
	lockPath := filepath.Join(m.dataRoot, "state", "locks", "toolchains", key+".lock")
	unlock, err := acquireFileLock(ctx, lockPath)
	if err != nil {
		return "", err
	}
	defer unlock()
	environment := m.baseEnvironment()
	target := tool + "@" + version
	readyPath := filepath.Join(m.dataRoot, "state", "toolchains", key+".ready")
	path, whereErr := m.runner.Run(ctx, "", environment, "mise", "where", target)
	if whereErr == nil && path != "" {
		path = strings.TrimSpace(path)
		if verifyErr := m.verifyToolchain(ctx, environment, target, tool); verifyErr == nil {
			if markerErr := saveAtomicFile(readyPath, []byte(path+"\n"), 0o600); markerErr != nil {
				return "", markerErr
			}
			return path, nil
		}
		_ = os.Remove(readyPath)
	}
	if _, err := m.runner.Run(ctx, "", environment, "mise", "install", target); err != nil {
		return "", err
	}
	path, err = m.runner.Run(ctx, "", environment, "mise", "where", target)
	if err != nil || strings.TrimSpace(path) == "" {
		return "", err
	}
	path = strings.TrimSpace(path)
	if err := m.verifyToolchain(ctx, environment, target, tool); err != nil {
		return "", err
	}
	if err := saveAtomicFile(readyPath, []byte(path+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (m *Manager) verifyToolchain(ctx context.Context, environment []string, target, tool string) error {
	executable := map[string]string{"python": "python", "node": "node", "go": "go", "rust": "rustc"}[tool]
	versionArgument := "--version"
	if tool == "go" {
		versionArgument = "version"
	}
	_, err := m.runner.Run(ctx, "", environment, "mise", "exec", target, "--", executable, versionArgument)
	return err
}

func (m *Manager) platformKey() string {
	sum := sha256.Sum256([]byte(platformLibc()))
	return runtime.GOOS + "-" + runtime.GOARCH + "-" + hex.EncodeToString(sum[:8])
}

func degraded(stage string, err error) Result {
	return Result{Status: "degraded", Diagnostics: []Diagnostic{{Stage: stage, Message: err.Error()}}}
}

func degradedWithFingerprints(stage string, err error, runtimeHash, dependencyHash string) Result {
	result := degraded(stage, err)
	result.RuntimeFingerprint = runtimeHash
	result.DependencyFingerprint = dependencyHash
	return result
}

func (m *Manager) workspaceKey(workspace string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(workspace)))
	return hex.EncodeToString(sum[:])
}

func (m *Manager) workspaceLockPath(workspace string) string {
	return filepath.Join(m.dataRoot, "state", "locks", "workspaces", m.workspaceKey(workspace)+".lock")
}

func (m *Manager) markerPath(workspace string) string {
	return filepath.Join(m.dataRoot, "state", "environments", m.workspaceKey(workspace)+".json")
}

func (m *Manager) ensureDirectories() error {
	for _, path := range []string{
		filepath.Join(m.dataRoot, "repo-cache"),
		filepath.Join(m.dataRoot, "workspaces", "github"),
		filepath.Join(m.dataRoot, "workspaces", "discord"),
		filepath.Join(m.dataRoot, "codex-homes"),
		filepath.Join(m.dataRoot, "toolchains", "mise"),
		filepath.Join(m.dataRoot, "toolchains", "rustup"),
		filepath.Join(m.dataRoot, "caches", "mise"),
		filepath.Join(m.dataRoot, "caches", "uv"),
		filepath.Join(m.dataRoot, "caches", "corepack"),
		filepath.Join(m.dataRoot, "caches", "pnpm"),
		filepath.Join(m.dataRoot, "caches", "go-mod"),
		filepath.Join(m.dataRoot, "caches", "go-build"),
		filepath.Join(m.dataRoot, "caches", "cargo", "registry"),
		filepath.Join(m.dataRoot, "caches", "cargo", "git"),
		filepath.Join(m.dataRoot, "caches", "sccache"),
		filepath.Join(m.dataRoot, "state", "locks"),
		filepath.Join(m.dataRoot, "state", "environments"),
	} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) ready(ctx context.Context, workspace string, spec Spec, runtimeHash, dependencyHash string) bool {
	data, err := os.ReadFile(m.markerPath(workspace))
	if err != nil {
		return false
	}
	var marker stateMarker
	if json.Unmarshal(data, &marker) != nil || marker.Status != "ready" ||
		marker.RuntimeFingerprint != runtimeHash || marker.DependencyFingerprint != dependencyHash {
		return false
	}
	for _, project := range spec.Projects {
		for _, dependency := range project.Dependencies {
			path := filepath.Join(workspace, filepath.FromSlash(project.Path))
			switch dependency.Manager {
			case "uv", "requirements":
				if !m.pythonEnvironmentMatches(ctx, path, m.baseEnvironment(), spec.Runtimes.Python) {
					return false
				}
			case "pnpm":
				if !exists(path, filepath.Join("node_modules", ".modules.yaml")) ||
					!exists("", m.pnpmCorepackExecutable(spec.Runtimes.PNPM)) {
					return false
				}
			case "go":
				environment := append(m.baseEnvironment(), "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local", "GOPROXY=off")
				if _, err := m.runner.Run(ctx, path, environment, "mise", "exec", "go@"+spec.Runtimes.Go,
					"--", "go", "mod", "download"); err != nil {
					return false
				}
			case "cargo":
				environment := append(m.baseEnvironment(), "CARGO_NET_OFFLINE=true")
				if _, err := m.runner.Run(ctx, path, environment, "mise", "exec", "rust@"+spec.Runtimes.Rust.Version,
					"--", "cargo", "fetch", "--locked", "--offline"); err != nil {
					return false
				}
			}
		}
	}
	return true
}

func (m *Manager) saveMarker(workspace string, marker stateMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return saveAtomicFile(m.markerPath(workspace), data, 0o600)
}

func (m *Manager) loadMarker(workspace string) stateMarker {
	data, err := os.ReadFile(m.markerPath(workspace))
	if err != nil {
		return stateMarker{}
	}
	var marker stateMarker
	if json.Unmarshal(data, &marker) != nil {
		return stateMarker{}
	}
	return marker
}

func saveAtomicFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	if err := os.WriteFile(temp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}
