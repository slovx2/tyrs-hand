package devenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveManifestAndMonorepo(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".tyrs-hand/runtime.yaml", `
version: 1
runtimes:
  python: "3.13.14"
  node: "24.14.0"
  pnpm: "11.14.0"
  rust:
    version: "1.97.1"
    profile: minimal
    components: [rustfmt]
    targets: []
projects:
  - name: api
    path: api
    dependencies:
      - manager: requirements
        files: [requirements.txt]
  - name: web
    path: web
    dependencies:
      - manager: pnpm
  - name: desktop
    path: src-tauri
    dependencies:
      - manager: cargo
`)
	for _, path := range []string{
		"api/requirements.txt", "web/package.json", "web/pnpm-lock.yaml",
		"src-tauri/Cargo.toml", "src-tauri/Cargo.lock",
	} {
		writeTestFile(t, root, path, "")
	}
	writeTestFile(t, root, "web/package.json", `{}`)
	lock, err := LoadRuntimeLock()
	require.NoError(t, err)
	spec, err := Resolve(root, lock)
	require.NoError(t, err)
	require.Equal(t, manifestPath, spec.Source)
	require.Len(t, spec.Projects, 3)
	require.Equal(t, "1.97.1", spec.Runtimes.Rust.Version)
}

func TestResolveAutoDetectionAndDefaults(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "pyproject.toml", "[project]\nname='demo'\n")
	writeTestFile(t, root, "uv.lock", "version = 1\n")
	writeTestFile(t, root, "package.json", `{"name":"demo","packageManager":"pnpm@11.14.0"}`)
	writeTestFile(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	lock, err := LoadRuntimeLock()
	require.NoError(t, err)
	spec, err := Resolve(root, lock)
	require.NoError(t, err)
	require.Equal(t, "auto", spec.Source)
	require.Equal(t, lock.Defaults.Python, spec.Runtimes.Python)
	require.Equal(t, lock.Defaults.Node, spec.Runtimes.Node)
	require.Equal(t, "11.14.0", spec.Runtimes.PNPM)
	require.Len(t, spec.Projects[0].Dependencies, 2)
}

func TestResolveRejectsInvalidDeclarations(t *testing.T) {
	lock, err := LoadRuntimeLock()
	require.NoError(t, err)
	tests := []struct {
		name     string
		manifest string
		files    []string
	}{
		{name: "unknown field", manifest: "version: 1\nunknown: true\n"},
		{name: "non exact", manifest: "version: 1\nruntimes:\n  python: '>=3.11'\nprojects:\n  - name: api\n    path: .\n    dependencies:\n      - manager: requirements\n        files: [requirements.txt]\n", files: []string{"requirements.txt"}},
		{name: "unknown manager", manifest: "version: 1\nprojects:\n  - name: api\n    path: .\n    dependencies:\n      - manager: pip\n"},
		{name: "path escape", manifest: "version: 1\nprojects:\n  - name: api\n    path: ../api\n    dependencies: []\n"},
		{name: "absolute path", manifest: "version: 1\nprojects:\n  - name: api\n    path: /tmp/api\n    dependencies: []\n"},
		{name: "missing path", manifest: "version: 1\nprojects:\n  - name: api\n    path: missing\n    dependencies: []\n"},
		{name: "duplicate yaml field", manifest: "version: 1\nversion: 1\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, ".tyrs-hand/runtime.yaml", test.manifest)
			for _, path := range test.files {
				writeTestFile(t, root, path, "")
			}
			_, err := Resolve(root, lock)
			require.Error(t, err)
		})
	}
}

func TestResolveRejectsNonExactNativeVersion(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "requirements.txt", "demo==1.0.0\n")
	writeTestFile(t, root, ".python-version", "3.13\n")
	lock, err := LoadRuntimeLock()
	require.NoError(t, err)
	_, err = Resolve(root, lock)
	require.ErrorContains(t, err, "不是完整精确版本")
}

func TestResolveAcceptsExactNVMVersionWithPrefix(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "package.json", `{"name":"demo","packageManager":"pnpm@11.14.0"}`)
	writeTestFile(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	writeTestFile(t, root, ".nvmrc", "v24.14.0\n")
	lock, err := LoadRuntimeLock()
	require.NoError(t, err)
	spec, err := Resolve(root, lock)
	require.NoError(t, err)
	require.Equal(t, "24.14.0", spec.Runtimes.Node)
}

func TestResolveRejectsEveryNonExactVersionForm(t *testing.T) {
	lock, err := LoadRuntimeLock()
	require.NoError(t, err)
	for _, version := range []string{"latest", "stable", "3", "3.13", ">=3.13.0", "3.13.*"} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "requirements.txt", "idna==3.10\n")
			writeTestFile(t, root, ".tyrs-hand/runtime.yaml", "version: 1\nruntimes:\n  python: \""+version+"\"\nprojects:\n  - name: api\n    path: .\n    dependencies: [{manager: requirements}]\n")
			_, err := Resolve(root, lock)
			require.ErrorContains(t, err, "不是完整精确版本")
		})
	}
}

func TestFingerprintsTrackOnlyEnvironmentInputs(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "requirements.txt", "demo==1.0.0\n")
	lock, err := LoadRuntimeLock()
	require.NoError(t, err)
	spec, err := Resolve(root, lock)
	require.NoError(t, err)
	first, err := dependencyFingerprint(root, spec)
	require.NoError(t, err)
	writeTestFile(t, root, "main.py", "print('changed')\n")
	second, err := dependencyFingerprint(root, spec)
	require.NoError(t, err)
	require.Equal(t, first, second)
	writeTestFile(t, root, "requirements.txt", "demo==2.0.0\n")
	third, err := dependencyFingerprint(root, spec)
	require.NoError(t, err)
	require.NotEqual(t, first, third)
}

func TestResolveRejectsSchemaAndProjectEdgeCases(t *testing.T) {
	lock, err := LoadRuntimeLock()
	require.NoError(t, err)
	tests := []struct {
		name     string
		manifest string
		files    []string
	}{
		{name: "yaml syntax", manifest: "version: [\n"},
		{name: "schema version", manifest: "version: 2\n"},
		{name: "duplicate manager", manifest: "version: 1\nprojects:\n  - name: api\n    path: .\n    dependencies:\n      - manager: requirements\n      - manager: requirements\n", files: []string{"requirements.txt"}},
		{name: "mutually exclusive python", manifest: "version: 1\nprojects:\n  - name: api\n    path: .\n    dependencies:\n      - manager: uv\n      - manager: requirements\n", files: []string{"pyproject.toml", "uv.lock", "requirements.txt"}},
		{name: "duplicate project path", manifest: "version: 1\nprojects:\n  - name: one\n    path: .\n    dependencies: []\n  - name: two\n    path: .\n    dependencies: []\n"},
		{name: "duplicate project name", manifest: "version: 1\nprojects:\n  - name: same\n    path: .\n    dependencies: []\n  - name: same\n    path: child\n    dependencies: []\n", files: []string{"child/.keep"}},
		{name: "invalid manager options", manifest: "version: 1\nprojects:\n  - name: web\n    path: .\n    dependencies:\n      - manager: pnpm\n        files: [package.json]\n", files: []string{"package.json", "pnpm-lock.yaml"}},
		{name: "rust arrays omitted", manifest: "version: 1\nruntimes:\n  rust:\n    version: 1.97.1\n    profile: minimal\nprojects: []\n"},
		{name: "invalid rust profile", manifest: "version: 1\nruntimes:\n  rust:\n    version: 1.97.1\n    profile: complete\n    components: []\n    targets: []\nprojects: []\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, ".tyrs-hand/runtime.yaml", test.manifest)
			for _, name := range test.files {
				writeTestFile(t, root, name, "")
			}
			_, err := Resolve(root, lock)
			require.Error(t, err)
		})
	}
}

func TestResolveDetectsSupportedRootManagers(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"pyproject.toml":      "[project]\nname='fixture'\nrequires-python='>=3.13'\n",
		"uv.lock":             "version = 1\n",
		"requirements.txt":    "ignored==1.0.0\n",
		"package.json":        `{"name":"fixture","packageManager":"pnpm@11.14.0+sha512.deadbeef"}`,
		"pnpm-lock.yaml":      "lockfileVersion: '9.0'\n",
		"go.mod":              "module example.test/fixture\n\ngo 1.26\ntoolchain go1.26.5\n",
		"Cargo.toml":          "[package]\nname='fixture'\nversion='0.1.0'\nrust-version='1.97'\n",
		"Cargo.lock":          "version = 4\n",
		"rust-toolchain.toml": "[toolchain]\nchannel='1.97.1'\nprofile='minimal'\ncomponents=['rustfmt']\ntargets=[]\n",
	}
	for name, contents := range files {
		writeTestFile(t, root, name, contents)
	}
	lock, err := LoadRuntimeLock()
	require.NoError(t, err)
	spec, err := Resolve(root, lock)
	require.NoError(t, err)
	managers := managerSet(spec.Projects)
	require.True(t, managers["uv"])
	require.False(t, managers["requirements"])
	require.True(t, managers["pnpm"])
	require.True(t, managers["go"])
	require.True(t, managers["cargo"])
	require.Equal(t, "11.14.0", spec.Runtimes.PNPM)
	require.Equal(t, []string{"rustfmt"}, spec.Runtimes.Rust.Components)
}

func TestResolveChecksManifestRuntimeCompatibility(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".tyrs-hand/runtime.yaml", `
version: 1
runtimes:
  python: "3.13.14"
  node: "24.14.0"
projects:
  - name: api
    path: api
    dependencies: [{manager: uv}]
  - name: web
    path: web
    dependencies: [{manager: pnpm}]
`)
	writeTestFile(t, root, "api/pyproject.toml", "[project]\nname='api'\nrequires-python='>=3.14'\n")
	writeTestFile(t, root, "api/uv.lock", "version = 1\n")
	writeTestFile(t, root, "web/package.json", `{"engines":{"node":">=25.0.0"}}`)
	writeTestFile(t, root, "web/pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	lock, err := LoadRuntimeLock()
	require.NoError(t, err)
	_, err = Resolve(root, lock)
	require.ErrorContains(t, err, "不满足")
}

func TestFingerprintsNormalizeDeclarationOrder(t *testing.T) {
	lock, err := LoadRuntimeLock()
	require.NoError(t, err)
	first := Spec{Version: 1, Runtimes: Runtimes{Rust: &RustRuntime{Version: "1.97.1", Profile: "minimal", Components: []string{"clippy", "rustfmt"}, Targets: []string{"b", "a"}}}}
	second := Spec{Version: 1, Runtimes: Runtimes{Rust: &RustRuntime{Version: "1.97.1", Profile: "minimal", Components: []string{"rustfmt", "clippy"}, Targets: []string{"a", "b"}}}}
	firstHash, err := runtimeFingerprint(first, lock)
	require.NoError(t, err)
	secondHash, err := runtimeFingerprint(second, lock)
	require.NoError(t, err)
	require.Equal(t, firstHash, secondHash)
}

func TestRuntimeFingerprintTracksRuntimeAndAdapterInputs(t *testing.T) {
	lock, err := LoadRuntimeLock()
	require.NoError(t, err)
	base := Spec{Version: 1, Runtimes: Runtimes{Python: "3.13.14", Rust: &RustRuntime{
		Version: "1.97.1", Profile: "minimal", Components: []string{"rustfmt"}, Targets: []string{},
	}}}
	baseHash, err := runtimeFingerprint(base, lock)
	require.NoError(t, err)

	changedRuntime := base
	changedRuntime.Runtimes.Python = "3.14.6"
	runtimeHash, err := runtimeFingerprint(changedRuntime, lock)
	require.NoError(t, err)
	require.NotEqual(t, baseHash, runtimeHash)

	changedTarget := base
	rust := *base.Runtimes.Rust
	rust.Targets = []string{"wasm32-unknown-unknown"}
	changedTarget.Runtimes.Rust = &rust
	targetHash, err := runtimeFingerprint(changedTarget, lock)
	require.NoError(t, err)
	require.NotEqual(t, baseHash, targetHash)

	changedAdapter := lock
	changedAdapter.AdapterSchemaVersion = "999"
	adapterHash, err := runtimeFingerprint(base, changedAdapter)
	require.NoError(t, err)
	require.NotEqual(t, baseHash, adapterHash)
}

func writeTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
}
