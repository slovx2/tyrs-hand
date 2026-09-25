package hostworker

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func registryFixture(root string) []RuntimeEntryOptions {
	entries := make([]RuntimeEntryOptions, 0, 2)
	for i, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		home := filepath.Join(root, string(engine))
		entries = append(entries, RuntimeEntryOptions{
			Runtime: RuntimeOptions{Engine: engine, WorkerID: "worker", StateDir: filepath.Join(home, "state"),
				CodexHome: filepath.Join(home, "config"), WorkspaceRoot: filepath.Join(root, "project"), EnvFile: filepath.Join(home, "runtime.env")},
			SSH: SSHOptions{ListenAddr: []string{"127.0.0.1:2222", "127.0.0.1:3333"}[i], HostKeyFile: filepath.Join(home, "host_key")},
		})
	}
	return entries
}

func TestRuntimeRegistryRejectsDifferentClientCredentials(t *testing.T) {
	entries := registryFixture(t.TempDir())
	for index := range entries {
		key, _, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		public, err := ssh.NewPublicKey(key)
		require.NoError(t, err)
		entries[index].SSH.AuthorizedClients = []AuthorizedClient{{ID: "same-client", PublicKey: public}}
	}
	require.ErrorContains(t, validateEntries(entries), "共用客户端授权凭证")
	entries[1].SSH.AuthorizedClients = entries[0].SSH.AuthorizedClients
	require.NoError(t, validateEntries(entries))
}

func TestRuntimeRegistryRejectsIdentityAndStorageConflicts(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, validateEntries(registryFixture(root)))
	for name, mutate := range map[string]func([]RuntimeEntryOptions){
		"worker":        func(entries []RuntimeEntryOptions) { entries[1].Runtime.WorkerID = "another" },
		"engine":        func(entries []RuntimeEntryOptions) { entries[1].Runtime.Engine = runtimeidentity.Codex },
		"unknown":       func(entries []RuntimeEntryOptions) { entries[1].Runtime.Engine = "unknown" },
		"state":         func(entries []RuntimeEntryOptions) { entries[1].Runtime.StateDir = entries[0].Runtime.StateDir },
		"credentials":   func(entries []RuntimeEntryOptions) { entries[1].Runtime.EnvFile = entries[0].Runtime.EnvFile },
		"host key":      func(entries []RuntimeEntryOptions) { entries[1].SSH.HostKeyFile = entries[0].SSH.HostKeyFile },
		"wildcard port": func(entries []RuntimeEntryOptions) { entries[1].SSH.ListenAddr = ":2222" },
		"hostname port": func(entries []RuntimeEntryOptions) { entries[1].SSH.ListenAddr = "localhost:2222" },
		"project":       func(entries []RuntimeEntryOptions) { entries[1].Runtime.WorkspaceRoot = "/different" },
	} {
		t.Run(name, func(t *testing.T) {
			entries := registryFixture(root)
			mutate(entries)
			require.Error(t, validateEntries(entries))
		})
	}
}

func TestRuntimeRegistryResolvesConfigurationSymlinks(t *testing.T) {
	root := t.TempDir()
	entries := registryFixture(root)
	real := filepath.Join(root, "actual")
	require.NoError(t, os.Mkdir(real, 0o700))
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(real, alias))
	entries[0].Runtime.StateDir = filepath.Join(real, "not-created")
	entries[1].Runtime.StateDir = filepath.Join(alias, "not-created")
	require.ErrorContains(t, validateEntries(entries), "必须独立")
}
