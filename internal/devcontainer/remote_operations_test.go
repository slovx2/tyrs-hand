package devcontainer

import (
	"context"
	"errors"
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
	mu           sync.Mutex
	calls        [][]string
	failContains string
	resultFor    map[string]string
}

func (r *recordingCommandRunner) Run(_ context.Context, _ []string, _ string,
	arguments ...string,
) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string(nil), arguments...))
	if r.failContains != "" && strings.Contains(strings.Join(arguments, " "), r.failContains) {
		return "", errors.New("injected command failure")
	}
	for fragment, result := range r.resultFor {
		if strings.Contains(strings.Join(arguments, " "), fragment) {
			return result, nil
		}
	}
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

func TestRefreshRemoteRuntimeOnlyWhenWorkerBinariesChange(t *testing.T) {
	root := t.TempDir()
	codexBin := filepath.Join(root, "codex-real")
	proxyBin := filepath.Join(root, "codex")
	require.NoError(t, os.WriteFile(codexBin, []byte("codex-v2"), 0o755))
	require.NoError(t, os.WriteFile(proxyBin, []byte("proxy-v2"), 0o755))
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		codexBin: codexBin, codexProxyBin: proxyBin}
	signature, err := manager.desiredRuntimeSignature()
	require.NoError(t, err)

	currentRunner := &recordingCommandRunner{resultFor: map[string]string{
		"cat " + runtimeSignaturePath: signature,
	}}
	manager.runner = currentRunner
	runtime := Runtime{Container: "dev-container", UID: 1000, GID: 1000, Home: "/home/dev"}
	changed, err := manager.RefreshRemoteRuntime(context.Background(), runtime)
	require.NoError(t, err)
	require.False(t, changed)
	require.False(t, currentRunner.contains("docker cp"))

	staleRunner := &recordingCommandRunner{resultFor: map[string]string{
		"cat " + runtimeSignaturePath: "old-signature",
	}}
	manager.runner = staleRunner
	changed, err = manager.RefreshRemoteRuntime(context.Background(), runtime)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, staleRunner.contains("app-server.pid"))
	require.True(t, staleRunner.contains("docker cp --follow-link "+codexBin))
	require.True(t, staleRunner.contains("TYRS_RUNTIME_SIGNATURE="+signature))
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
	require.True(t, runner.contains("chown \"$TYRS_OWNER\" /run/tyrs-hand"))
	require.True(t, runner.contains("chmod 0700 /run/tyrs-hand"))
	require.True(t, runner.contains("chmod 0777 /run/tyrs-hand"))
	require.True(t, runner.contains("chmod 0666 /run/tyrs-hand/app-server.sock"))
	require.True(t, runner.contains("docker exec --detach --user 0:0"))
	require.True(t, runner.contains("sshd -D -e"))
	require.True(t, runner.contains("codex-real app-server --listen unix:///run/tyrs-hand/app-server.sock"))
}

func TestShareAppServerSocketFallsBackToHostPermissions(t *testing.T) {
	runtimeRoot := t.TempDir()
	environmentID := uuid.New()
	environmentRuntime := filepath.Join(runtimeRoot, environmentID.String())
	require.NoError(t, os.MkdirAll(environmentRuntime, 0o770))
	socketPath := filepath.Join(environmentRuntime, "app-server.sock")
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))
	runner := &recordingCommandRunner{failContains: "chmod 0666 /run/tyrs-hand/app-server.sock"}
	manager := &Manager{dockerBin: "docker", dockerHost: "inherit", runner: runner,
		developmentRuntimeDir: runtimeRoot}

	require.NoError(t, manager.shareAppServerSocket(context.Background(), "development",
		environmentID))
	metadata, err := os.Stat(socketPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o666), metadata.Mode().Perm())

	require.ErrorContains(t, manager.shareAppServerSocket(context.Background(), "development",
		uuid.New()), "宿主回退")

	setupFailure := &Manager{dockerBin: "docker", dockerHost: "inherit",
		runner: &recordingCommandRunner{failContains: "chown \"$TYRS_WORKER_OWNER\""}}
	require.ErrorContains(t, setupFailure.shareAppServerSocket(context.Background(),
		"development", environmentID), "共享环境 Codex")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	waitFailure := &Manager{dockerBin: "docker", dockerHost: "inherit",
		runner: &recordingCommandRunner{failContains: "test -S /run/tyrs-hand/app-server.sock"}}
	require.ErrorIs(t, waitFailure.waitForAppServerSocket(canceled, "development"),
		context.Canceled)
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
