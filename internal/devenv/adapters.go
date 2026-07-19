package devenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

func (m *Manager) prepareDependency(
	ctx context.Context,
	workspace string,
	spec Spec,
	project Project,
	dependency Dependency,
	toolPaths map[string]string,
	runtimeChanged bool,
) error {
	projectPath := filepath.Join(workspace, filepath.FromSlash(project.Path))
	environment := m.baseEnvironment()
	switch dependency.Manager {
	case "uv":
		python := filepath.Join(toolPaths["python"], "bin", "python")
		environment = append(environment, "UV_PYTHON="+python)
		if err := m.ensurePythonEnvironment(ctx, projectPath, environment, python, spec.Runtimes.Python); err != nil {
			return err
		}
		args := []string{"sync", "--frozen"}
		for _, group := range dependency.Groups {
			args = append(args, "--group", group)
		}
		_, err := m.runner.Run(ctx, projectPath, environment, "uv", args...)
		return err
	case "requirements":
		python := filepath.Join(toolPaths["python"], "bin", "python")
		environment = append(environment, "UV_PYTHON="+python)
		if err := m.ensurePythonEnvironment(ctx, projectPath, environment, python, spec.Runtimes.Python); err != nil {
			return err
		}
		args := []string{"pip", "sync", "--python", filepath.Join(projectPath, ".venv", "bin", "python")}
		args = append(args, dependency.Files...)
		_, err := m.runner.Run(ctx, projectPath, environment, "uv", args...)
		return err
	case "pnpm":
		target := "node@" + spec.Runtimes.Node
		pnpm := filepath.Join(toolPaths["pnpm"], "bin", "pnpm")
		baseArgs := []string{"exec", target, "--", pnpm, "install", "--frozen-lockfile",
			"--store-dir", filepath.Join(m.dataRoot, "caches", "pnpm"), "--trust-lockfile"}
		if runtimeChanged {
			baseArgs = append(baseArgs, "--force")
		}
		offlineArgs := append(append([]string(nil), baseArgs...), "--offline")
		if _, err := m.runner.Run(ctx, projectPath, environment, "mise", offlineArgs...); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return err
		}
		onlineArgs := append(baseArgs, "--prefer-offline")
		_, err := m.runner.Run(ctx, projectPath, environment, "mise", onlineArgs...)
		return err
	case "go":
		environment = append(environment, "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local")
		_, err := m.runner.Run(ctx, projectPath, environment, "mise", "exec", "go@"+spec.Runtimes.Go, "--", "go", "mod", "download")
		return err
	case "cargo":
		_, err := m.runner.Run(ctx, projectPath, environment, "mise", "exec",
			"rust@"+spec.Runtimes.Rust.Version, "--", "cargo", "fetch", "--locked")
		return err
	default:
		return fmt.Errorf("不支持的依赖 Manager %q", dependency.Manager)
	}
}

func (m *Manager) ensurePythonEnvironment(
	ctx context.Context,
	projectPath string,
	environment []string,
	python string,
	version string,
) error {
	if m.pythonEnvironmentMatches(ctx, projectPath, environment, version) {
		return nil
	}
	_, err := m.runner.Run(ctx, projectPath, environment, "uv", "venv", "--python", python, "--clear", ".venv")
	return err
}

func (m *Manager) pythonEnvironmentMatches(
	ctx context.Context,
	projectPath string,
	environment []string,
	version string,
) bool {
	venvPython := filepath.Join(projectPath, ".venv", "bin", "python")
	if !exists("", venvPython) || !exists(projectPath, filepath.Join(".venv", "pyvenv.cfg")) {
		return false
	}
	output, err := m.runner.Run(ctx, projectPath, environment, venvPython, "--version")
	return err == nil && strings.TrimSpace(output) == "Python "+version
}

func (m *Manager) ensureRustToolchain(ctx context.Context, rust RustRuntime) (string, error) {
	payload, err := json.Marshal(normalizedRuntimes(Runtimes{Rust: &rust}).Rust)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	specKey := rust.Version + "-" + hex.EncodeToString(sum[:8])
	toolchainKey := "rust-" + rust.Version + "-" + m.platformKey()
	unlock, err := acquireFileLock(ctx, filepath.Join(m.dataRoot, "state", "locks", "toolchains", toolchainKey+".lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	environment := m.baseEnvironment()
	target := "rust@" + rust.Version
	readyPath := filepath.Join(m.dataRoot, "state", "toolchains", toolchainKey+"-"+specKey+".ready")
	path, whereErr := m.runner.Run(ctx, "", environment, "mise", "where", target)
	if whereErr == nil && strings.TrimSpace(path) != "" {
		if _, statErr := os.Stat(readyPath); statErr == nil &&
			m.verifyToolchain(ctx, environment, target, "rust") == nil &&
			m.verifyRustExtras(ctx, environment, target, rust) == nil {
			return strings.TrimSpace(path), nil
		}
	}
	configData, err := toml.Marshal(map[string]any{"tools": map[string]any{"rust": map[string]any{
		"version": rust.Version, "profile": rust.Profile, "components": rust.Components, "targets": rust.Targets,
	}}})
	if err != nil {
		return "", err
	}
	configPath := filepath.Join(m.dataRoot, "state", "mise-specs", "rust-"+specKey+".toml")
	if err := saveAtomicFile(configPath, configData, 0o600); err != nil {
		return "", err
	}
	environment = append(environment, "MISE_CONFIG_FILE="+configPath)
	if _, err := m.runner.Run(ctx, "", environment, "mise", "install"); err != nil {
		return "", err
	}
	path, err = m.runner.Run(ctx, "", environment, "mise", "where", target)
	if err != nil || strings.TrimSpace(path) == "" {
		return "", err
	}
	if err := m.verifyToolchain(ctx, environment, target, "rust"); err != nil {
		return "", err
	}
	if err := m.verifyRustExtras(ctx, environment, target, rust); err != nil {
		return "", err
	}
	if err := saveAtomicFile(readyPath, []byte(strings.TrimSpace(path)+"\n"), 0o600); err != nil {
		return "", err
	}
	return strings.TrimSpace(path), nil
}

func (m *Manager) verifyRustExtras(ctx context.Context, environment []string, target string, rust RustRuntime) error {
	if len(rust.Components) > 0 {
		output, err := m.runner.Run(ctx, "", environment, "mise", "exec", target, "--", "rustup", "component", "list", "--installed")
		if err != nil {
			return err
		}
		for _, component := range rust.Components {
			if !containsRustupItem(output, component) {
				return fmt.Errorf("rust component %s 未安装", component)
			}
		}
	}
	if len(rust.Targets) > 0 {
		output, err := m.runner.Run(ctx, "", environment, "mise", "exec", target, "--", "rustup", "target", "list", "--installed")
		if err != nil {
			return err
		}
		for _, target := range rust.Targets {
			if !containsRustupItem(output, target) {
				return fmt.Errorf("rust target %s 未安装", target)
			}
		}
	}
	return nil
}

func containsRustupItem(output, expected string) bool {
	for _, line := range strings.Split(output, "\n") {
		name := strings.Fields(strings.TrimSpace(line))
		if len(name) > 0 && (name[0] == expected || strings.HasPrefix(name[0], expected+"-")) {
			return true
		}
	}
	return false
}

func (m *Manager) ensurePNPM(ctx context.Context, nodeVersion, pnpmVersion string) (string, error) {
	key := pnpmVersion + "-" + m.platformKey()
	root := filepath.Join(m.dataRoot, "toolchains", "pnpm", key)
	unlock, err := acquireFileLock(ctx, filepath.Join(m.dataRoot, "state", "locks", "toolchains", "pnpm-"+key+".lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	environment := m.baseEnvironment()
	if !exists("", m.pnpmCorepackExecutable(pnpmVersion)) {
		if _, err := m.runner.Run(ctx, "", environment, "mise", "exec", "node@"+nodeVersion,
			"--", "corepack", "pnpm@"+pnpmVersion, "--version"); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o750); err != nil {
		return "", err
	}
	temp, err := os.MkdirTemp(filepath.Dir(root), ".pnpm-"+key+"-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	bin := filepath.Join(temp, "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		return "", err
	}
	script := "#!/bin/sh\nexec node " + shellQuote(m.pnpmCorepackExecutable(pnpmVersion)) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "pnpm"), []byte(script), 0o750); err != nil {
		return "", err
	}
	if err := os.RemoveAll(root); err != nil {
		return "", err
	}
	if err := os.Rename(temp, root); err != nil {
		return "", err
	}
	return root, nil
}

func missingTool(manager string, failures map[string]error) bool {
	required := map[string][]string{
		"uv": {"python"}, "requirements": {"python"}, "pnpm": {"node", "pnpm"},
		"go": {"go"}, "cargo": {"rust"},
	}
	for _, tool := range required[manager] {
		if _, failed := failures[tool]; failed {
			return true
		}
	}
	return false
}

func (m *Manager) environment(ctx context.Context, workspace string, spec Spec, failed map[string]bool) ([]string, error) {
	paths := make([]string, 0, 8)
	var virtualEnvironment string
	var uvPython string
	var rustToolchain string
	var missing []string
	for _, project := range spec.Projects {
		for _, dependency := range project.Dependencies {
			if dependency.Manager == "pnpm" && !failed[dependencyKey(project, dependency)] {
				nodeModules := filepath.Join(workspace, filepath.FromSlash(project.Path), "node_modules")
				if exists(nodeModules, ".modules.yaml") {
					paths = append(paths, filepath.Join(nodeModules, ".bin"))
				}
			}
			if dependency.Manager != "uv" && dependency.Manager != "requirements" {
				continue
			}
			if failed[dependencyKey(project, dependency)] {
				continue
			}
			venv := filepath.Join(workspace, filepath.FromSlash(project.Path), ".venv")
			if !exists(venv, filepath.Join("bin", "python")) || !exists(venv, "pyvenv.cfg") {
				continue
			}
			if virtualEnvironment == "" {
				virtualEnvironment = venv
			}
			paths = append(paths, filepath.Join(venv, "bin"))
		}
	}
	tools := []struct {
		name    string
		version string
	}{
		{"python", spec.Runtimes.Python}, {"node", spec.Runtimes.Node}, {"go", spec.Runtimes.Go},
	}
	if spec.Runtimes.Rust != nil {
		tools = append(tools, struct {
			name    string
			version string
		}{"rust", spec.Runtimes.Rust.Version})
	}
	if spec.Runtimes.PNPM != "" {
		paths = append(paths, filepath.Join(m.pnpmToolchainPath(spec.Runtimes.PNPM), "bin"))
	}
	for _, tool := range tools {
		if tool.version == "" {
			continue
		}
		path, err := m.runner.Run(ctx, "", m.baseEnvironment(), "mise", "where", tool.name+"@"+tool.version)
		if err != nil {
			missing = append(missing, tool.name+"@"+tool.version)
			continue
		}
		toolPath := strings.TrimSpace(path)
		paths = append(paths, filepath.Join(toolPath, "bin"))
		if tool.name == "python" {
			uvPython = filepath.Join(toolPath, "bin", "python")
		}
		if tool.name == "rust" {
			rustToolchain = tool.version
		}
	}
	paths = append(paths, filepath.Join(m.dataRoot, "caches", "cargo", "bin"), os.Getenv("PATH"))
	environment := m.baseEnvironment()
	environment = append(environment, "PATH="+strings.Join(paths, string(os.PathListSeparator)))
	if virtualEnvironment != "" {
		environment = append(environment, "VIRTUAL_ENV="+virtualEnvironment)
	}
	if uvPython != "" {
		environment = append(environment, "UV_PYTHON="+uvPython)
	}
	if rustToolchain != "" {
		environment = append(environment, "RUSTUP_TOOLCHAIN="+rustToolchain)
	}
	if len(missing) > 0 {
		return environment, fmt.Errorf("以下 Toolchain 不可用: %s", strings.Join(missing, ", "))
	}
	return environment, nil
}

func (m *Manager) baseEnvironment() []string {
	platform := m.platformKey()
	values := map[string]string{
		"MISE_DATA_DIR":              filepath.Join(m.dataRoot, "toolchains", "mise", platform),
		"MISE_CACHE_DIR":             filepath.Join(m.dataRoot, "caches", "mise"),
		"MISE_STATE_DIR":             filepath.Join(m.dataRoot, "state", "mise", platform),
		"MISE_CONFIG_DIR":            filepath.Join(m.dataRoot, "state", "mise-config", platform),
		"MISE_RUSTUP_HOME":           filepath.Join(m.dataRoot, "toolchains", "rustup", platform),
		"MISE_CARGO_HOME":            filepath.Join(m.dataRoot, "caches", "cargo"),
		"RUSTUP_HOME":                filepath.Join(m.dataRoot, "toolchains", "rustup", platform),
		"CARGO_HOME":                 filepath.Join(m.dataRoot, "caches", "cargo"),
		"UV_CACHE_DIR":               filepath.Join(m.dataRoot, "caches", "uv"),
		"COREPACK_HOME":              filepath.Join(m.dataRoot, "caches", "corepack"),
		"GOMODCACHE":                 filepath.Join(m.dataRoot, "caches", "go-mod"),
		"GOCACHE":                    filepath.Join(m.dataRoot, "caches", "go-build"),
		"GOTOOLCHAIN":                "local",
		"SCCACHE_DIR":                filepath.Join(m.dataRoot, "caches", "sccache"),
		"RUSTC_WRAPPER":              "sccache",
		"PNPM_STORE_DIR":             filepath.Join(m.dataRoot, "caches", "pnpm"),
		"COREPACK_DEFAULT_TO_LATEST": "0",
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func (m *Manager) pnpmToolchainPath(version string) string {
	return filepath.Join(m.dataRoot, "toolchains", "pnpm", version+"-"+m.platformKey())
}

func (m *Manager) pnpmCorepackExecutable(version string) string {
	return filepath.Join(m.dataRoot, "caches", "corepack", "v1", "pnpm", version, "bin", "pnpm.cjs")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
