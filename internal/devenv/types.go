package devenv

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

//go:embed worker-runtime.lock.json
var runtimeLockJSON []byte

type RuntimeLock struct {
	SchemaVersion        int               `json:"schemaVersion"`
	AdapterSchemaVersion string            `json:"adapterSchemaVersion"`
	Codex                string            `json:"codex"`
	Mise                 string            `json:"mise"`
	UV                   string            `json:"uv"`
	Corepack             string            `json:"corepack"`
	Downloads            RuntimeDownloads  `json:"downloads"`
	SystemPackages       map[string]string `json:"systemPackages"`
	Defaults             RuntimeDefaults   `json:"defaults"`
}

type RuntimeDownloads struct {
	Mise map[string]string `json:"mise"`
	UV   map[string]string `json:"uv"`
}

type RuntimeDefaults struct {
	Python string `json:"python"`
	Node   string `json:"node"`
	PNPM   string `json:"pnpm"`
	Go     string `json:"go"`
	Rust   string `json:"rust"`
}

func LoadRuntimeLock() (RuntimeLock, error) {
	var lock RuntimeLock
	if err := json.Unmarshal(runtimeLockJSON, &lock); err != nil {
		return RuntimeLock{}, fmt.Errorf("解析 Worker Runtime Lock: %w", err)
	}
	if lock.SchemaVersion != 1 || lock.AdapterSchemaVersion == "" ||
		lock.Codex == "" || lock.Mise == "" || lock.UV == "" || lock.Corepack == "" ||
		len(lock.Downloads.Mise) == 0 || len(lock.Downloads.UV) == 0 || len(lock.SystemPackages) == 0 {
		return RuntimeLock{}, fmt.Errorf("Worker Runtime Lock 版本无效")
	}
	versions := map[string]string{
		"codex": lock.Codex, "mise": lock.Mise, "uv": lock.UV, "corepack": lock.Corepack,
		"python": lock.Defaults.Python, "node": lock.Defaults.Node, "pnpm": lock.Defaults.PNPM,
		"go": lock.Defaults.Go, "rust": lock.Defaults.Rust,
	}
	for name, version := range versions {
		if !exactVersion.MatchString(version) {
			return RuntimeLock{}, fmt.Errorf("Worker Runtime Lock 的 %s 必须是精确版本", name)
		}
	}
	checksum := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, architecture := range []string{"amd64", "arm64"} {
		if !checksum.MatchString(lock.Downloads.Mise[architecture]) || !checksum.MatchString(lock.Downloads.UV[architecture]) {
			return RuntimeLock{}, fmt.Errorf("Worker Runtime Lock 缺少 %s 下载校验值", architecture)
		}
	}
	for name, version := range lock.SystemPackages {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" || strings.ContainsAny(version, "*?[] ") {
			return RuntimeLock{}, fmt.Errorf("Worker 系统包 %q 必须固定精确版本", name)
		}
	}
	return lock, nil
}

type Spec struct {
	Version  int       `json:"version" yaml:"version"`
	Runtimes Runtimes  `json:"runtimes" yaml:"runtimes"`
	Projects []Project `json:"projects" yaml:"projects"`
	Source   string    `json:"source" yaml:"-"`
}

type Runtimes struct {
	Python string       `json:"python,omitempty" yaml:"python,omitempty"`
	Node   string       `json:"node,omitempty" yaml:"node,omitempty"`
	PNPM   string       `json:"pnpm,omitempty" yaml:"pnpm,omitempty"`
	Go     string       `json:"go,omitempty" yaml:"go,omitempty"`
	Rust   *RustRuntime `json:"rust,omitempty" yaml:"rust,omitempty"`
}

type RustRuntime struct {
	Version    string   `json:"version" yaml:"version"`
	Profile    string   `json:"profile" yaml:"profile"`
	Components []string `json:"components" yaml:"components"`
	Targets    []string `json:"targets" yaml:"targets"`
}

type Project struct {
	Name         string       `json:"name" yaml:"name"`
	Path         string       `json:"path" yaml:"path"`
	Dependencies []Dependency `json:"dependencies" yaml:"dependencies"`
}

type Dependency struct {
	Manager string   `json:"manager" yaml:"manager"`
	Groups  []string `json:"groups,omitempty" yaml:"groups,omitempty"`
	Files   []string `json:"files,omitempty" yaml:"files,omitempty"`
}

type Diagnostic struct {
	Stage   string `json:"stage"`
	Project string `json:"project,omitempty"`
	Manager string `json:"manager,omitempty"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type Result struct {
	Status                string          `json:"status"`
	RuntimeFingerprint    string          `json:"runtimeFingerprint,omitempty"`
	DependencyFingerprint string          `json:"dependencyFingerprint,omitempty"`
	Environment           []string        `json:"environment,omitempty"`
	Projects              []ProjectResult `json:"projects,omitempty"`
	Diagnostics           []Diagnostic    `json:"diagnostics,omitempty"`
	PreparedAt            *time.Time      `json:"preparedAt,omitempty"`
}

type ProjectResult struct {
	Name         string             `json:"name"`
	Path         string             `json:"path"`
	Status       string             `json:"status"`
	Dependencies []DependencyResult `json:"dependencies,omitempty"`
}

type DependencyResult struct {
	Manager string `json:"manager"`
	Status  string `json:"status"`
}
