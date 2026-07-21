package devcontainer

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
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
	require.True(t, runner.contains("/var/lib/tyrs-hand/codex/"+conversationID.String()))

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

	for _, operation := range []RemoteOperation{
		{Operation: "start", ContainerName: "dev-container"},
		{Operation: "stop", ContainerName: "dev-container"},
		{Operation: "rebuild", ContainerName: "dev-container", ImageRef: "image-ref"},
	} {
		require.NoError(t, manager.RunRemoteOperation(context.Background(), operation))
	}
	require.True(t, runner.contains("docker stop --time 10 dev-container"))
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
	require.Equal(t, "/var/lib/tyrs-hand/codex/"+conversationID.String(), runtime.CodexHome)
	require.Equal(t, "ok", state.WorkspaceHeadSHA)
	require.True(t, state.WorkspaceDirty)
	require.True(t, runner.contains("docker start dev-container"))
	require.True(t, runner.contains("mkdir -p "+runtime.CodexHome))
	require.True(t, runner.contains("chown 1000:1000 "+runtime.CodexHome))
	require.True(t, runner.contains("git status --porcelain=v1"))
	require.True(t, runner.contains("git rev-parse HEAD"))
}

func TestEnsureRemoteRequiresDevelopmentContainers(t *testing.T) {
	manager := &Manager{}
	_, _, err := manager.EnsureRemote(context.Background(), RemoteSpec{}, "")
	require.ErrorContains(t, err, "未启用")
}
