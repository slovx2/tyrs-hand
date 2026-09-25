package bootstrap

import (
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func TestWorkerRuntimeEntriesShareCredentialButSeparateState(t *testing.T) {
	cfg := config.Config{WorkerDataRoot: t.TempDir(), WorkerClaudeBin: "/test/claude",
		WorkerClaudeEnabled: true, WorkerSSHListenAddr: ":2222", WorkerClaudeSSHListenAddr: ":3333"}
	controller := appserverhub.PassThroughController{}
	codex := hostworker.RuntimeOptions{Engine: runtimeidentity.Codex, WorkerID: "one-worker",
		CodexBin: "/test/codex", Home: "/home/user", CodexHome: "/home/user/.codex",
		StateDir: cfg.WorkerDataRoot, WorkspaceRoot: "/projects", SSHAuthSock: "/ssh/agent.sock",
		Controller: controller, BrowserServiceSocket: "/browser/proxy.sock"}
	ssh := hostworker.SSHOptions{ListenAddr: cfg.WorkerSSHListenAddr,
		HostKeyFile:       filepath.Join(cfg.WorkerDataRoot, "ssh", "host_key"),
		AuthorizedClients: []hostworker.AuthorizedClient{{ID: "same-client"}}}
	entries := workerRuntimeEntries(cfg, codex, ssh, controller)
	require.Len(t, entries, 2)
	claude := entries[1]
	require.Equal(t, ":2222", entries[0].SSH.ListenAddr)
	require.Equal(t, ":3333", claude.SSH.ListenAddr)
	require.Equal(t, entries[0].SSH.AuthorizedClients, claude.SSH.AuthorizedClients)
	require.Equal(t, codex.WorkerID, claude.Runtime.WorkerID)
	require.Equal(t, codex.WorkspaceRoot, claude.Runtime.WorkspaceRoot)
	require.Equal(t, codex.SSHAuthSock, claude.Runtime.SSHAuthSock)
	require.Empty(t, claude.Runtime.BrowserServiceSocket)
	require.Equal(t, runtimeidentity.Claude, claude.Runtime.Engine)
	require.NotEqual(t, codex.StateDir, claude.Runtime.StateDir)
	require.NotEqual(t, codex.Home, claude.Runtime.Home)
	require.NotEqual(t, codex.CodexHome, claude.Runtime.CodexHome)
	require.NotEqual(t, ssh.HostKeyFile, claude.SSH.HostKeyFile)
	require.Equal(t, filepath.Join(claude.Runtime.CodexHome, "claude"), cfg.ClaudeConfigDir())
	cfg.WorkerClaudeEnabled = false
	require.Len(t, workerRuntimeEntries(cfg, codex, ssh, nil), 1)
}
