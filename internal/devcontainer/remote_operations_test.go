package devcontainer

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

type recordingCommandRunner struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *recordingCommandRunner) Run(_ context.Context, _ []string, _ string,
	arguments ...string,
) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), arguments...))
	return "ok", nil
}

func (r *recordingCommandRunner) contains(parts ...string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	expected := strings.Join(parts, " ")
	for _, call := range r.calls {
		if strings.Contains(strings.Join(call, " "), expected) {
			return true
		}
	}
	return false
}

func TestRunRemoteDevelopmentOperations(t *testing.T) {
	runner := &recordingCommandRunner{}
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit", runner: runner}
	conversationID := uuid.New()
	err := manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "delete_forum", ContainerName: "dev-container",
		Workspace: "workspaces/forum", ConversationIDs: []uuid.UUID{conversationID},
	})
	require.NoError(t, err)
	require.True(t, runner.contains("docker container inspect dev-container"))
	require.True(t, runner.contains("docker start dev-container"))
	require.True(t, runner.contains("/var/lib/tyrs-hand/workspaces/forum"))
	require.False(t, runner.contains("/var/lib/tyrs-hand/codex/"+conversationID.String()))

	err = manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "delete_environment", ContainerName: "dev-container", ImageRef: "image-ref",
		DataVolume: "data-volume", HomeVolume: "home-volume", Network: "dev-network",
	})
	require.NoError(t, err)
	for _, expected := range [][]string{
		{"docker", "container", "rm", "--force", "dev-container"},
		{"docker", "volume", "rm", "data-volume"},
		{"docker", "volume", "rm", "home-volume"},
		{"docker", "network", "rm", "dev-network"},
		{"docker", "image", "rm", "image-ref"},
	} {
		require.True(t, runner.contains(expected...))
	}

	require.NoError(t, manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "rebuild", ContainerName: "dev-container", ImageRef: "image-ref",
	}))
	require.Error(t, manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "start", ContainerName: "dev-container",
	}))
	require.Error(t, manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "stop", ContainerName: "dev-container",
	}))
}

func TestReconfigureRemoteEnvironmentKeepsContainerRunningAndSecuresSSH(t *testing.T) {
	runner := &recordingCommandRunner{}
	runtimeDir := t.TempDir()
	codexBin := filepath.Join(runtimeDir, "codex-real")
	proxyBin := filepath.Join(runtimeDir, "codex")
	require.NoError(t, os.WriteFile(codexBin, []byte("mock"), 0o755))
	require.NoError(t, os.WriteFile(proxyBin, []byte("mock"), 0o755))
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit", runner: runner,
		developmentRuntimeDir: runtimeDir, developmentRuntimeHostDir: "/host/runtime",
		codexBin: codexBin, codexProxyBin: proxyBin}
	environmentID := uuid.New()
	err := manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "reconfigure", EnvironmentID: environmentID,
		ContainerName: "dev-container", ImageRef: "dev-image", DataVolume: "dev-data",
		HomeVolume: "dev-home", Network: "dev-network", RuntimeUser: "agent",
		RuntimeUID: 1000, RuntimeGID: 1000, RuntimeHome: "/home/agent",
		SSHPort: 2222, SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest",
		SSHConfigRevision: 3,
	})
	require.NoError(t, err)
	require.True(t, runner.contains("docker create"))
	require.True(t, runner.contains("--restart unless-stopped"))
	require.True(t, runner.contains("--publish 2222:22"))
	require.True(t, runner.contains("type=bind,source=/host/runtime/"+environmentID.String()+",target=/run/tyrs-hand"))
	require.True(t, runner.contains("PasswordAuthentication no"))
	require.True(t, runner.contains("PermitRootLogin no"))
	require.True(t, runner.contains("DisableForwarding yes"))
	require.True(t, runner.contains("AuthenticationMethods publickey"))
	require.True(t, runner.contains("docker exec --detach --user 0:0"))
	require.True(t, runner.contains("sshd -D -e"))
	require.True(t, runner.contains("codex-real app-server --listen unix:///run/tyrs-hand/app-server.sock"))
}

func TestRunRemoteDevelopmentOperationRejectsUnknownType(t *testing.T) {
	disabled := &Manager{}
	require.ErrorContains(t, disabled.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "start",
	}), "未启用")
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: &recordingCommandRunner{}}
	require.Error(t, manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "unknown",
	}))
}

func TestEnsureRemoteUsesExistingEnvironment(t *testing.T) {
	runner := &recordingCommandRunner{}
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit", runner: runner}
	environmentID, forumID, conversationID := uuid.New(), uuid.New(), uuid.New()
	runtime, state, err := manager.EnsureRemote(context.Background(), RemoteSpec{
		EnvironmentID: environmentID, ForumID: forumID, ConversationID: conversationID,
		WorkspaceStatus: "ready", WorkspaceRelative: "workspaces/forum", WorkspaceBranch: "main",
		Repository: "owner/repo", CloneURL: "https://example.invalid/owner/repo.git",
		DefaultRef: "main", EnvironmentStatus: "running", ImageRef: "dev-image",
		ImageID: "sha256:image", ContainerName: "dev-container", ContainerID: "container-id",
		DataVolume: "dev-data", HomeVolume: "dev-home", Network: "dev-network",
		RuntimeUser: "agent", RuntimeUID: 1000, RuntimeGID: 1000, RuntimeHome: "/home/agent",
	}, "credential")
	require.NoError(t, err)
	require.Equal(t, environmentID, runtime.EnvironmentID)
	require.Equal(t, forumID, runtime.ForumID)
	require.Equal(t, "dev-container", runtime.Container)
	require.Equal(t, "/var/lib/tyrs-hand/workspaces/forum", runtime.Workspace)
	require.Equal(t, "/var/lib/tyrs-hand/codex", runtime.CodexHome)
	require.Equal(t, "ok", state.WorkspaceHeadSHA)
	require.True(t, state.WorkspaceDirty)
	require.True(t, runner.contains("docker start dev-container"))
	require.True(t, runner.contains("mkdir -p "+runtime.CodexHome))
	require.True(t, runner.contains("chown 1000:1000 "+runtime.CodexHome))
	require.True(t, runner.contains("git status --porcelain=v1"))
	require.True(t, runner.contains("git rev-parse HEAD"))

	second, _, err := manager.EnsureRemote(context.Background(), RemoteSpec{
		EnvironmentID: environmentID, ForumID: forumID, ConversationID: uuid.New(),
		WorkspaceStatus: "ready", WorkspaceRelative: "workspaces/forum", WorkspaceBranch: "main",
		EnvironmentStatus: "running", ContainerName: "dev-container", ContainerID: "container-id",
		RuntimeUser: "agent", RuntimeUID: 1000, RuntimeGID: 1000, RuntimeHome: "/home/agent",
	}, "credential")
	require.NoError(t, err)
	require.Equal(t, runtime.CodexHome, second.CodexHome)
}

func TestEnsureRemoteRequiresDevelopmentContainers(t *testing.T) {
	manager := &Manager{}
	_, _, err := manager.EnsureRemote(context.Background(), RemoteSpec{}, "")
	require.ErrorContains(t, err, "未启用")
}

func TestCoordinateRemoteStartsPermanentDaemons(t *testing.T) {
	runner := &recordingCommandRunner{}
	runtimeDir, err := os.MkdirTemp("/tmp", "tyrs-coordinate-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: runner, developmentRuntimeDir: runtimeDir}
	manifest := workerprotocol.EnvironmentManifest{
		EnvironmentID: uuid.New(), ContainerName: "dev-container", RuntimeUser: "agent",
		RuntimeUID: 1000, RuntimeGID: 1000, RuntimeHome: "/home/agent",
		SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest", SSHPort: 2222,
	}
	runtime, err := manager.CoordinateRemote(context.Background(), manifest)
	require.NoError(t, err)
	require.Equal(t, "/var/lib/tyrs-hand/codex", runtime.CodexHome)
	require.Equal(t, filepath.Join(manager.developmentRuntimeDir,
		manifest.EnvironmentID.String(), "app-server.sock"), runtime.AppServerSocket)
	require.True(t, runner.contains("docker start dev-container"))
	require.True(t, runner.contains("codex-real app-server --listen"))
	require.True(t, runner.contains("sshd -D -e"))
	listener, err := net.Listen("unix", runtime.AppServerSocket)
	require.NoError(t, err)
	require.NoError(t, manager.EnsureRemoteDaemons(context.Background(), manifest, runtime))
	require.NoError(t, listener.Close())
	require.True(t, runner.contains("sshd.pid"))
	require.NoError(t, manager.StopRemoteAppServer(context.Background(), manifest.ContainerName))

	_, err = (&Manager{}).PrepareRemoteRuntime(context.Background(), manifest)
	require.ErrorContains(t, err, "未启用")
	manifest.RuntimeUID = 0
	_, err = manager.PrepareRemoteRuntime(context.Background(), manifest)
	require.ErrorContains(t, err, "Manifest")
}
