package devenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeCommandRunner struct {
	mu           sync.Mutex
	installed    map[string]bool
	installs     int
	dependencies int
	failCommand  string
	venvVersion  string
}

func (f *fakeCommandRunner) Run(_ context.Context, dir string, _ []string, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	command := name + " " + strings.Join(args, " ")
	if f.failCommand != "" && strings.Contains(command, f.failCommand) {
		return "", errors.New("simulated command failure")
	}
	if name == "mise" && len(args) >= 2 && args[0] == "where" {
		if !f.installed[args[1]] {
			return "", errors.New("not installed")
		}
		return filepath.Join("/toolchains", strings.ReplaceAll(args[1], "@", "/")), nil
	}
	if name == "mise" && len(args) >= 2 && args[0] == "install" {
		f.installed[args[1]] = true
		f.installs++
		return "", nil
	}
	if name == "uv" && len(args) > 0 && args[0] == "venv" {
		if err := os.MkdirAll(filepath.Join(dir, ".venv", "bin"), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, ".venv", "bin", "python"), []byte{}, 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, ".venv", "pyvenv.cfg"), []byte("home = /toolchains/python\n"), 0o600); err != nil {
			return "", err
		}
		f.venvVersion = filepath.Base(filepath.Dir(filepath.Dir(args[2])))
		f.dependencies++
		return "", nil
	}
	if strings.HasSuffix(name, filepath.Join(".venv", "bin", "python")) && len(args) == 1 && args[0] == "--version" {
		return "Python " + f.venvVersion, nil
	}
	if name == "uv" {
		f.dependencies++
		return "", nil
	}
	if name == "mise" && len(args) > 0 && args[0] == "exec" {
		if dir != "" && strings.Contains(command, "pnpm") && strings.Contains(command, " install ") {
			_ = os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
			_ = os.WriteFile(filepath.Join(dir, "node_modules", ".modules.yaml"), []byte("layoutVersion: 5\n"), 0o600)
		}
		if strings.Contains(command, "cargo fetch") || strings.Contains(command, "go mod download") ||
			(strings.Contains(command, "pnpm") && strings.Contains(command, " install ")) {
			f.dependencies++
		}
		return "", nil
	}
	return "", nil
}

func TestManagerPreparesAndReusesWorkspace(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "requirements.txt", "demo==1.0.0\n")
	runner := &fakeCommandRunner{installed: make(map[string]bool)}
	manager, err := NewManager(t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	manager.runner = runner

	first := manager.Prepare(context.Background(), root)
	require.Equal(t, "ready", first.Status)
	require.NotEmpty(t, first.RuntimeFingerprint)
	require.FileExists(t, filepath.Join(root, ".venv", "bin", "python"))
	require.Contains(t, strings.Join(first.Environment, "\n"), "GOTOOLCHAIN=local")
	require.Contains(t, strings.Join(first.Environment, "\n"), "UV_PYTHON=/toolchains/python/3.13.14/bin/python")
	installs, dependencies := runner.installs, runner.dependencies

	second := manager.Prepare(context.Background(), root)
	require.Equal(t, "ready", second.Status)
	require.Equal(t, installs, runner.installs)
	require.Equal(t, dependencies, runner.dependencies)
}

func TestManagerRebuildsMissingLocalEnvironment(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "requirements.txt", "demo==1.0.0\n")
	runner := &fakeCommandRunner{installed: make(map[string]bool)}
	manager, err := NewManager(t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	manager.runner = runner
	require.Equal(t, "ready", manager.Prepare(context.Background(), root).Status)
	firstDependencies := runner.dependencies
	require.NoError(t, os.Remove(filepath.Join(root, ".venv", "bin", "python")))

	result := manager.Prepare(context.Background(), root)
	require.Equal(t, "ready", result.Status)
	require.FileExists(t, filepath.Join(root, ".venv", "bin", "python"))
	require.Greater(t, runner.dependencies, firstDependencies)
}

func TestManagerRebuildsPythonEnvironmentWithWrongVersion(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "requirements.txt", "demo==1.0.0\n")
	runner := &fakeCommandRunner{installed: make(map[string]bool)}
	manager, err := NewManager(t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	manager.runner = runner
	require.Equal(t, "ready", manager.Prepare(context.Background(), root).Status)
	firstDependencies := runner.dependencies
	runner.venvVersion = "3.12.0"

	result := manager.Prepare(context.Background(), root)
	require.Equal(t, "ready", result.Status)
	require.Equal(t, "3.13.14", runner.venvVersion)
	require.Greater(t, runner.dependencies, firstDependencies)
}

func TestManagerSerializesSameWorkspacePreparation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "requirements.txt", "demo==1.0.0\n")
	runner := &fakeCommandRunner{installed: make(map[string]bool)}
	manager, err := NewManager(t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	manager.runner = runner

	results := make(chan Result, 6)
	var wait sync.WaitGroup
	for range 6 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- manager.Prepare(context.Background(), root)
		}()
	}
	wait.Wait()
	close(results)
	for result := range results {
		require.Equal(t, "ready", result.Status)
	}
	runner.mu.Lock()
	require.Equal(t, 1, runner.installs)
	require.Equal(t, 2, runner.dependencies)
	runner.mu.Unlock()
}

func TestManagerDegradesWithoutBlockingOnManifestError(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".tyrs-hand/runtime.yaml", "version: 1\nunknown: true\n")
	manager, err := NewManager(t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	manager.runner = &fakeCommandRunner{installed: make(map[string]bool)}
	result := manager.Prepare(context.Background(), root)
	require.Equal(t, "degraded", result.Status)
	require.NotEmpty(t, result.Diagnostics)
}

func TestManagerDegradesOnDependencyFailure(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "requirements.txt", "demo==1.0.0\n")
	manager, err := NewManager(t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	manager.runner = &fakeCommandRunner{installed: make(map[string]bool), failCommand: "pip sync"}
	result := manager.Prepare(context.Background(), root)
	require.Equal(t, "degraded", result.Status)
	require.NotEmpty(t, result.Environment)
	require.Equal(t, "dependencies", result.Diagnostics[0].Stage)
}

func TestManagerKeepsSuccessfulProjectsWhenOneFails(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".tyrs-hand/runtime.yaml", `
version: 1
runtimes:
  python: "3.13.14"
  node: "24.14.0"
  pnpm: "11.14.0"
projects:
  - name: api
    path: api
    dependencies: [{manager: requirements}]
  - name: web
    path: web
    dependencies: [{manager: pnpm}]
`)
	writeTestFile(t, root, "api/requirements.txt", "demo==1.0.0\n")
	writeTestFile(t, root, "web/package.json", `{"name":"fixture","packageManager":"pnpm@11.14.0"}`)
	writeTestFile(t, root, "web/pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	runner := &fakeCommandRunner{installed: make(map[string]bool), failCommand: "pip sync"}
	manager, err := NewManager(t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	manager.runner = runner

	result := manager.Prepare(context.Background(), root)
	require.Equal(t, "degraded", result.Status)
	require.FileExists(t, filepath.Join(root, "web", "node_modules", ".modules.yaml"))
	require.Contains(t, strings.Join(result.Environment, "\n"), filepath.Join("toolchains", "pnpm"))
	require.Contains(t, strings.Join(result.Environment, "\n"), filepath.Join("web", "node_modules", ".bin"))
	require.NotContains(t, strings.Join(result.Environment, "\n"), filepath.Join("api", ".venv", "bin"))
	require.Len(t, result.Diagnostics, 1)
	require.Equal(t, "api", result.Diagnostics[0].Project)
	require.Equal(t, "degraded", result.Projects[0].Status)
	require.Equal(t, "ready", result.Projects[1].Status)
}

func TestManagerInstallsSharedToolchainOnceAcrossConcurrentWorkspaces(t *testing.T) {
	runner := &fakeCommandRunner{installed: make(map[string]bool)}
	manager, err := NewManager(t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	manager.runner = runner
	workspaces := make([]string, 6)
	for index := range workspaces {
		workspaces[index] = t.TempDir()
		writeTestFile(t, workspaces[index], "requirements.txt", "demo==1.0.0\n")
	}
	var wait sync.WaitGroup
	results := make(chan Result, len(workspaces))
	for _, workspace := range workspaces {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- manager.Prepare(context.Background(), workspace)
		}()
	}
	wait.Wait()
	close(results)
	for result := range results {
		require.Equal(t, "ready", result.Status)
	}
	runner.mu.Lock()
	require.Equal(t, 1, runner.installs)
	runner.mu.Unlock()
}

func TestManagerPreparesPNPMAndExposesExactWrapper(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "package.json", `{"name":"fixture","packageManager":"pnpm@11.14.0"}`)
	writeTestFile(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	runner := &fakeCommandRunner{installed: make(map[string]bool)}
	manager, err := NewManager(t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	manager.runner = runner
	result := manager.Prepare(context.Background(), root)
	require.Equal(t, "ready", result.Status)
	require.FileExists(t, filepath.Join(root, "node_modules", ".modules.yaml"))
	var pathValue string
	for _, value := range result.Environment {
		if strings.HasPrefix(value, "PATH=") {
			pathValue = strings.TrimPrefix(value, "PATH=")
		}
	}
	require.Contains(t, pathValue, filepath.Join("toolchains", "pnpm", "11.14.0-"))
}

func TestManagerPNPMUsesFrozenTrustedLockfile(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "package.json", `{"name":"fixture","packageManager":"pnpm@11.14.0"}`)
	writeTestFile(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	runner := &recordingCommandRunner{fakeCommandRunner: fakeCommandRunner{installed: make(map[string]bool)}}
	manager, err := NewManager(t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	manager.runner = runner

	result := manager.Prepare(context.Background(), root)
	require.Equal(t, "ready", result.Status)
	require.Contains(t, strings.Join(runner.commands, "\n"),
		"install --frozen-lockfile --store-dir")
	require.Contains(t, strings.Join(runner.commands, "\n"), "--trust-lockfile --offline")
}

func TestManagerForcesPNPMRematerializationAfterNodeChange(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "package.json", `{"name":"fixture","packageManager":"pnpm@11.14.0"}`)
	writeTestFile(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	writeTestFile(t, root, ".tyrs-hand/runtime.yaml", "version: 1\nruntimes:\n  node: 24.14.0\n  pnpm: 11.14.0\nprojects:\n  - name: web\n    path: .\n    dependencies: [{manager: pnpm}]\n")
	runner := &recordingCommandRunner{fakeCommandRunner: fakeCommandRunner{installed: make(map[string]bool)}}
	manager, err := NewManager(t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	manager.runner = runner
	require.Equal(t, "ready", manager.Prepare(context.Background(), root).Status)
	writeTestFile(t, root, ".tyrs-hand/runtime.yaml", "version: 1\nruntimes:\n  node: 22.23.1\n  pnpm: 11.14.0\nprojects:\n  - name: web\n    path: .\n    dependencies: [{manager: pnpm}]\n")

	result := manager.Prepare(context.Background(), root)
	require.Equal(t, "ready", result.Status)
	require.Contains(t, strings.Join(runner.commands, "\n"), "--trust-lockfile --force --offline")
}

type recordingCommandRunner struct {
	fakeCommandRunner
	commands []string
}

func (r *recordingCommandRunner) Run(ctx context.Context, dir string, environment []string, name string, args ...string) (string, error) {
	r.commands = append(r.commands, name+" "+strings.Join(args, " "))
	return r.fakeCommandRunner.Run(ctx, dir, environment, name, args...)
}

type contextCommandRunner struct{}

func (contextCommandRunner) Run(ctx context.Context, _ string, _ []string, _ string, _ ...string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestManagerTimeoutReturnsDegraded(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "requirements.txt", "demo==1.0.0\n")
	manager, err := NewManager(t.TempDir(), zap.NewNop())
	require.NoError(t, err)
	manager.runner = contextCommandRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := manager.Prepare(ctx, root)
	require.Equal(t, "degraded", result.Status)
	require.NotEmpty(t, result.Diagnostics)
}
