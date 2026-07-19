package devenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func runtimeFingerprint(spec Spec, lock RuntimeLock) (string, error) {
	payload := struct {
		Spec         Runtimes
		Lock         RuntimeLock
		OS           string
		Architecture string
		OSRelease    string
		Libc         string
		ImageDigest  string
	}{
		Spec: normalizedRuntimes(spec.Runtimes), Lock: lock, OS: runtime.GOOS, Architecture: runtime.GOARCH,
		OSRelease: platformOSRelease(), Libc: platformLibc(),
		ImageDigest: os.Getenv("TYRS_HAND_WORKER_IMAGE_DIGEST"),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func dependencyFingerprint(root string, spec Spec) (string, error) {
	files := make(map[string]struct{})
	if spec.Source == manifestPath {
		files[manifestPath] = struct{}{}
	}
	for _, project := range spec.Projects {
		for _, dependency := range project.Dependencies {
			for _, name := range dependencyFiles(dependency) {
				files[filepath.ToSlash(filepath.Join(project.Path, name))] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	specJSON, err := json.Marshal(normalizedSpec(spec))
	if err != nil {
		return "", err
	}
	_, _ = hash.Write(specJSON)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if os.IsNotExist(err) && filepath.Base(name) == "go.sum" {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("读取依赖输入 %s: %w", name, err)
		}
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func normalizedSpec(spec Spec) Spec {
	copySpec := spec
	copySpec.Runtimes = normalizedRuntimes(spec.Runtimes)
	copySpec.Projects = append([]Project(nil), spec.Projects...)
	for projectIndex := range copySpec.Projects {
		project := &copySpec.Projects[projectIndex]
		project.Dependencies = append([]Dependency(nil), project.Dependencies...)
		for dependencyIndex := range project.Dependencies {
			dependency := &project.Dependencies[dependencyIndex]
			dependency.Groups = sortedCopy(dependency.Groups)
			dependency.Files = sortedCopy(dependency.Files)
		}
		sort.Slice(project.Dependencies, func(i, j int) bool {
			return project.Dependencies[i].Manager < project.Dependencies[j].Manager
		})
	}
	sort.Slice(copySpec.Projects, func(i, j int) bool {
		if copySpec.Projects[i].Path == copySpec.Projects[j].Path {
			return copySpec.Projects[i].Name < copySpec.Projects[j].Name
		}
		return copySpec.Projects[i].Path < copySpec.Projects[j].Path
	})
	return copySpec
}

func normalizedRuntimes(runtimes Runtimes) Runtimes {
	copyRuntimes := runtimes
	if runtimes.Rust != nil {
		rust := *runtimes.Rust
		rust.Components = sortedCopy(rust.Components)
		rust.Targets = sortedCopy(rust.Targets)
		copyRuntimes.Rust = &rust
	}
	return copyRuntimes
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func platformOSRelease() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	lines := make([]string, 0, 2)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "ID=") || strings.HasPrefix(line, "VERSION_ID=") {
			lines = append(lines, line)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func platformLibc() string {
	output, err := exec.Command("ldd", "--version").CombinedOutput()
	if err != nil {
		return "unknown"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	return line
}

func dependencyFiles(dependency Dependency) []string {
	switch dependency.Manager {
	case "uv":
		return []string{"pyproject.toml", "uv.lock"}
	case "requirements":
		return append([]string(nil), dependency.Files...)
	case "pnpm":
		return []string{"package.json", "pnpm-lock.yaml"}
	case "go":
		return []string{"go.mod", "go.sum"}
	case "cargo":
		return []string{"Cargo.toml", "Cargo.lock"}
	default:
		return nil
	}
}
