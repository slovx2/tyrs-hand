package devenv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	pep440 "github.com/aquasecurity/go-pep440-version"
	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"
)

const manifestPath = ".tyrs-hand/runtime.yaml"

var exactVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func Resolve(root string, lock RuntimeLock) (Spec, error) {
	spec, declared, err := readManifest(root)
	if err != nil {
		return Spec{}, err
	}
	if !declared {
		spec = Spec{Version: 1, Source: "auto"}
	} else {
		spec.Source = manifestPath
	}
	rustDeclared := declared && spec.Runtimes.Rust != nil
	if spec.Version != 1 {
		return Spec{}, fmt.Errorf("%s 的 version 必须为 1", manifestPath)
	}
	if len(spec.Projects) == 0 {
		spec.Projects = detectRootProject(root)
	}
	if err := validateProjects(root, spec.Projects); err != nil {
		return Spec{}, err
	}
	if err := resolveVersions(root, &spec, lock.Defaults); err != nil {
		return Spec{}, err
	}
	if spec.Runtimes.Rust != nil {
		if rustDeclared && (spec.Runtimes.Rust.Components == nil || spec.Runtimes.Rust.Targets == nil) {
			return Spec{}, fmt.Errorf("Rust components 和 targets 必须明确列出，可使用空数组")
		}
		if spec.Runtimes.Rust.Profile == "" {
			spec.Runtimes.Rust.Profile = "minimal"
		}
		if spec.Runtimes.Rust.Profile != "minimal" && spec.Runtimes.Rust.Profile != "default" {
			return Spec{}, fmt.Errorf("Rust profile 只支持 minimal 或 default")
		}
		if spec.Runtimes.Rust.Components == nil {
			spec.Runtimes.Rust.Components = []string{}
		}
		if spec.Runtimes.Rust.Targets == nil {
			spec.Runtimes.Rust.Targets = []string{}
		}
		if err := validateUniqueStrings("Rust components", spec.Runtimes.Rust.Components); err != nil {
			return Spec{}, err
		}
		if err := validateUniqueStrings("Rust targets", spec.Runtimes.Rust.Targets); err != nil {
			return Spec{}, err
		}
	}
	if err := validateRuntimeCompatibility(root, spec); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func readManifest(root string) (Spec, bool, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(manifestPath)))
	if errors.Is(err, os.ErrNotExist) {
		return Spec{}, false, nil
	}
	if err != nil {
		return Spec{}, false, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var spec Spec
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, true, fmt.Errorf("解析 %s: %w", manifestPath, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("只能包含一个 YAML 文档")
		}
		return Spec{}, true, fmt.Errorf("解析 %s: %w", manifestPath, err)
	}
	return spec, true, nil
}

func detectRootProject(root string) []Project {
	dependencies := make([]Dependency, 0, 5)
	if exists(root, "uv.lock") && exists(root, "pyproject.toml") {
		dependencies = append(dependencies, Dependency{Manager: "uv"})
	} else if exists(root, "requirements.txt") {
		dependencies = append(dependencies, Dependency{Manager: "requirements", Files: []string{"requirements.txt"}})
	}
	if exists(root, "pnpm-lock.yaml") && exists(root, "package.json") {
		dependencies = append(dependencies, Dependency{Manager: "pnpm"})
	}
	if exists(root, "go.mod") {
		dependencies = append(dependencies, Dependency{Manager: "go"})
	}
	if exists(root, "Cargo.toml") && exists(root, "Cargo.lock") {
		dependencies = append(dependencies, Dependency{Manager: "cargo"})
	}
	if len(dependencies) == 0 {
		return nil
	}
	return []Project{{Name: "root", Path: ".", Dependencies: dependencies}}
}

func validateProjects(root string, projects []Project) error {
	seenPaths := make(map[string]struct{})
	seenNames := make(map[string]struct{})
	rootPath, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	for index := range projects {
		project := &projects[index]
		project.Name = strings.TrimSpace(project.Name)
		if project.Name == "" {
			return fmt.Errorf("Project name 不能为空")
		}
		if _, exists := seenNames[project.Name]; exists {
			return fmt.Errorf("Project name %q 重复", project.Name)
		}
		seenNames[project.Name] = struct{}{}
		if project.Path == "" {
			project.Path = "."
		}
		clean := filepath.Clean(filepath.FromSlash(project.Path))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("Project %q 的 path 必须位于仓库内", project.Name)
		}
		projectPath := filepath.Join(root, clean)
		info, err := os.Stat(projectPath)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("Project %q 的目录 %q 不存在", project.Name, project.Path)
		}
		resolvedPath, err := filepath.EvalSymlinks(projectPath)
		if err != nil || !pathWithin(rootPath, resolvedPath) {
			return fmt.Errorf("Project %q 的目录 %q 必须位于仓库内", project.Name, project.Path)
		}
		project.Path = filepath.ToSlash(clean)
		if _, ok := seenPaths[project.Path]; ok {
			return fmt.Errorf("Project path %q 重复", project.Path)
		}
		seenPaths[project.Path] = struct{}{}
		managers := make(map[string]struct{})
		for depIndex := range project.Dependencies {
			dep := &project.Dependencies[depIndex]
			switch dep.Manager {
			case "uv", "requirements", "pnpm", "go", "cargo":
			default:
				return fmt.Errorf("Project %q 使用了不支持的 Manager %q", project.Name, dep.Manager)
			}
			if _, ok := managers[dep.Manager]; ok {
				return fmt.Errorf("Project %q 重复声明 Manager %q", project.Name, dep.Manager)
			}
			managers[dep.Manager] = struct{}{}
			if dep.Manager != "uv" && len(dep.Groups) > 0 {
				return fmt.Errorf("Project %q 的 Manager %q 不支持 groups", project.Name, dep.Manager)
			}
			if dep.Manager != "requirements" && len(dep.Files) > 0 {
				return fmt.Errorf("Project %q 的 Manager %q 不支持 files", project.Name, dep.Manager)
			}
			if err := validateUniqueStrings("Project "+project.Name+" 的 groups", dep.Groups); err != nil {
				return err
			}
			if err := validateUniqueStrings("Project "+project.Name+" 的 files", dep.Files); err != nil {
				return err
			}
			if dep.Manager == "requirements" {
				if len(dep.Files) == 0 {
					dep.Files = []string{"requirements.txt"}
				}
				for _, name := range dep.Files {
					if err := requireFile(root, clean, name); err != nil {
						return fmt.Errorf("Project %q: %w", project.Name, err)
					}
				}
			}
		}
		if _, uv := managers["uv"]; uv {
			if _, requirements := managers["requirements"]; requirements {
				return fmt.Errorf("Project %q 不能同时使用 uv 和 requirements", project.Name)
			}
		}
		if err := validateProjectFiles(root, *project); err != nil {
			return err
		}
	}
	return nil
}

func validateProjectFiles(root string, project Project) error {
	required := map[string][]string{
		"uv": {"pyproject.toml", "uv.lock"}, "pnpm": {"package.json", "pnpm-lock.yaml"},
		"go": {"go.mod"}, "cargo": {"Cargo.toml", "Cargo.lock"},
	}
	for _, dep := range project.Dependencies {
		for _, name := range required[dep.Manager] {
			if err := requireFile(root, filepath.FromSlash(project.Path), name); err != nil {
				return fmt.Errorf("Project %q: %w", project.Name, err)
			}
		}
	}
	return nil
}

func resolveVersions(root string, spec *Spec, defaults RuntimeDefaults) error {
	managers := managerSet(spec.Projects)
	var err error
	spec.Runtimes.Python, err = resolveVersion(spec.Runtimes.Python, nativePython(root), defaults.Python, managers["uv"] || managers["requirements"])
	if err != nil {
		return fmt.Errorf("Python: %w", err)
	}
	nodeNeeded := managers["pnpm"] || strings.TrimSpace(spec.Runtimes.PNPM) != ""
	spec.Runtimes.Node, err = resolveVersion(spec.Runtimes.Node, nativeNode(root), defaults.Node, nodeNeeded)
	if err != nil {
		return fmt.Errorf("Node: %w", err)
	}
	spec.Runtimes.PNPM, err = resolveVersion(spec.Runtimes.PNPM, nativePNPM(root), defaults.PNPM, managers["pnpm"])
	if err != nil {
		return fmt.Errorf("pnpm: %w", err)
	}
	spec.Runtimes.Go, err = resolveVersion(spec.Runtimes.Go, nativeGo(root), defaults.Go, managers["go"])
	if err != nil {
		return fmt.Errorf("Go: %w", err)
	}
	if managers["cargo"] || spec.Runtimes.Rust != nil {
		native := nativeRust(root)
		if spec.Runtimes.Rust == nil {
			if native != nil {
				copyNative := *native
				spec.Runtimes.Rust = &copyNative
			} else {
				spec.Runtimes.Rust = &RustRuntime{Components: []string{}, Targets: []string{}}
			}
		}
		var nativeVersion *string
		if native != nil {
			nativeVersion = &native.Version
		}
		spec.Runtimes.Rust.Version, err = resolveVersion(spec.Runtimes.Rust.Version, nativeVersion, defaults.Rust, true)
		if err != nil {
			return fmt.Errorf("Rust: %w", err)
		}
	}
	return nil
}

func resolveVersion(configured string, native *string, fallback string, needed bool) (string, error) {
	value := strings.TrimSpace(configured)
	if value == "" && native != nil {
		value = strings.TrimSpace(*native)
	}
	if value == "" && needed {
		value = fallback
	}
	if value != "" && !exactVersion.MatchString(value) {
		return "", fmt.Errorf("版本 %q 不是完整精确版本", value)
	}
	return value, nil
}

func managerSet(projects []Project) map[string]bool {
	result := make(map[string]bool)
	for _, project := range projects {
		for _, dependency := range project.Dependencies {
			result[dependency.Manager] = true
		}
	}
	return result
}

func nativePython(root string) *string { return firstText(root, ".python-version") }
func nativeNode(root string) *string {
	value := firstText(root, ".node-version", ".nvmrc")
	if value == nil {
		return nil
	}
	normalized := strings.TrimPrefix(*value, "v")
	return &normalized
}
func nativeRust(root string) *RustRuntime {
	if value := firstText(root, "rust-toolchain"); value != nil {
		return &RustRuntime{Version: *value, Profile: "minimal", Components: []string{}, Targets: []string{}}
	}
	data, err := os.ReadFile(filepath.Join(root, "rust-toolchain.toml"))
	if err != nil {
		return nil
	}
	var native struct {
		Toolchain struct {
			Channel    string   `toml:"channel"`
			Profile    string   `toml:"profile"`
			Components []string `toml:"components"`
			Targets    []string `toml:"targets"`
		} `toml:"toolchain"`
	}
	if toml.Unmarshal(data, &native) != nil || native.Toolchain.Channel == "" {
		return nil
	}
	return &RustRuntime{Version: native.Toolchain.Channel, Profile: native.Toolchain.Profile,
		Components: native.Toolchain.Components, Targets: native.Toolchain.Targets}
}

func nativePNPM(root string) *string {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil
	}
	var value struct {
		PackageManager string `json:"packageManager"`
	}
	if json.Unmarshal(data, &value) != nil || !strings.HasPrefix(value.PackageManager, "pnpm@") {
		return nil
	}
	version := strings.TrimPrefix(value.PackageManager, "pnpm@")
	version, _, _ = strings.Cut(version, "+")
	return &version
}

func nativeGo(root string) *string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil
	}
	var fallback string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[0] == "toolchain" {
			version := strings.TrimPrefix(fields[1], "go")
			return &version
		}
		if fields[0] == "go" {
			fallback = fields[1]
		}
	}
	if fallback == "" {
		return nil
	}
	return &fallback
}

func firstText(root string, names ...string) *string {
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err == nil {
			value := strings.TrimSpace(string(data))
			return &value
		}
	}
	return nil
}

func exists(root, name string) bool {
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
	return err == nil && !info.IsDir()
}

func requireFile(root, projectPath, name string) error {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("文件路径 %q 无效", name)
	}
	projectRoot := filepath.Join(root, projectPath)
	target := filepath.Join(projectRoot, clean)
	if !exists(projectRoot, clean) {
		return fmt.Errorf("缺少文件 %q", filepath.ToSlash(filepath.Join(projectPath, clean)))
	}
	resolvedRoot, rootErr := filepath.EvalSymlinks(root)
	resolvedTarget, targetErr := filepath.EvalSymlinks(target)
	if rootErr != nil || targetErr != nil || !pathWithin(resolvedRoot, resolvedTarget) {
		return fmt.Errorf("文件路径 %q 必须位于仓库内", name)
	}
	return nil
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validateUniqueStrings(label string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%s 不能包含空值", label)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s 包含重复值 %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateRuntimeCompatibility(root string, spec Spec) error {
	for _, project := range spec.Projects {
		projectRoot := filepath.Join(root, filepath.FromSlash(project.Path))
		for _, dependency := range project.Dependencies {
			var err error
			switch dependency.Manager {
			case "uv":
				err = validatePythonCompatibility(projectRoot, spec.Runtimes.Python)
			case "pnpm":
				err = validateNodeCompatibility(projectRoot, spec.Runtimes.Node)
			case "go":
				err = validateGoCompatibility(projectRoot, spec.Runtimes.Go)
			case "cargo":
				err = validateRustCompatibility(projectRoot, spec.Runtimes.Rust.Version)
			}
			if err != nil {
				return fmt.Errorf("Project %q: %w", project.Name, err)
			}
		}
	}
	return nil
}

func validatePythonCompatibility(projectRoot, runtimeVersion string) error {
	data, err := os.ReadFile(filepath.Join(projectRoot, "pyproject.toml"))
	if err != nil {
		return err
	}
	var document struct {
		Project struct {
			RequiresPython string `toml:"requires-python"`
		} `toml:"project"`
	}
	if err := toml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("解析 pyproject.toml: %w", err)
	}
	requirement := strings.TrimSpace(document.Project.RequiresPython)
	if requirement == "" {
		return nil
	}
	specifiers, err := pep440.NewSpecifiers(requirement)
	if err != nil {
		return fmt.Errorf("requires-python %q 无效: %w", requirement, err)
	}
	version, err := pep440.Parse(runtimeVersion)
	if err != nil || !specifiers.Check(version) {
		return fmt.Errorf("Python %s 不满足 requires-python %q", runtimeVersion, requirement)
	}
	return nil
}

func validateNodeCompatibility(projectRoot, runtimeVersion string) error {
	data, err := os.ReadFile(filepath.Join(projectRoot, "package.json"))
	if err != nil {
		return err
	}
	var document struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("解析 package.json: %w", err)
	}
	requirement := strings.TrimSpace(document.Engines.Node)
	if requirement == "" {
		return nil
	}
	constraint, err := semver.NewConstraint(requirement)
	if err != nil {
		return fmt.Errorf("engines.node %q 无效: %w", requirement, err)
	}
	version, err := semver.NewVersion(runtimeVersion)
	if err != nil || !constraint.Check(version) {
		return fmt.Errorf("Node %s 不满足 engines.node %q", runtimeVersion, requirement)
	}
	return nil
}

func validateGoCompatibility(projectRoot, runtimeVersion string) error {
	data, err := os.ReadFile(filepath.Join(projectRoot, "go.mod"))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "go" {
			continue
		}
		return validateMinimumSemver("Go", runtimeVersion, fields[1])
	}
	return nil
}

func validateRustCompatibility(projectRoot, runtimeVersion string) error {
	data, err := os.ReadFile(filepath.Join(projectRoot, "Cargo.toml"))
	if err != nil {
		return err
	}
	var document struct {
		Package struct {
			RustVersion string `toml:"rust-version"`
		} `toml:"package"`
	}
	if err := toml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("解析 Cargo.toml: %w", err)
	}
	minimum := strings.TrimSpace(document.Package.RustVersion)
	if minimum == "" {
		return nil
	}
	return validateMinimumSemver("Rust", runtimeVersion, minimum)
}

func validateMinimumSemver(tool, runtimeVersion, minimum string) error {
	normalizedMinimum := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(minimum), "go"), "v")
	if strings.Count(normalizedMinimum, ".") == 1 {
		normalizedMinimum += ".0"
	}
	actual, actualErr := semver.NewVersion(runtimeVersion)
	required, requiredErr := semver.NewVersion(normalizedMinimum)
	if actualErr != nil || requiredErr != nil {
		return fmt.Errorf("%s 兼容版本 %q 无效", tool, minimum)
	}
	if actual.LessThan(required) {
		return fmt.Errorf("%s %s 低于项目要求的 %s", tool, runtimeVersion, minimum)
	}
	return nil
}
