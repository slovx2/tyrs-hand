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

func TestInstallSSHConfigurationReportsPartialUpdates(t *testing.T) {
	tests := []struct {
		name         string
		source       string
		failContains string
	}{
		{name: "本地配置不可读", source: "directory"},
		{name: "配置无法复制", source: "file", failContains: "99-tyrs-hand.conf.tmp"},
		{name: "默认配置无法创建", failContains: "printf 'Host *"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			agentDir := filepath.Join(root, "ssh-agent")
			require.NoError(t, os.Mkdir(agentDir, 0o755))
			source := filepath.Join(agentDir, "ssh_config")
			switch test.source {
			case "directory":
				require.NoError(t, os.Mkdir(source, 0o755))
			case "file":
				require.NoError(t, os.WriteFile(source, []byte("Host *\n"), 0o644))
			}
			manager := &Manager{dockerBin: "docker", dockerHost: "inherit",
				sshAgentDir: agentDir,
				runner:      &recordingCommandRunner{failContains: test.failContains}}
			require.Error(t, manager.installSSHConfiguration(context.Background(),
				"development", "/home/dev", "1000:1000"))
		})
	}
}

func TestConfigureRemoteDaemonsReportsStartFailures(t *testing.T) {
	tests := []struct {
		name         string
		failContains string
		publicKey    string
	}{
		{name: "初始化失败", failContains: "TYRS_UID="},
		{name: "SSH 启动失败", failContains: "/usr/sbin/sshd", publicKey: "ssh-ed25519 test"},
		{name: "app-server 启动失败", failContains: "tyrs-hand-app-server"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := &Manager{dockerBin: "docker", dockerHost: "inherit",
				runner: &recordingCommandRunner{failContains: test.failContains}}
			err := manager.configureRemoteDaemons(context.Background(), "development",
				RemoteOperation{RuntimeUser: "dev", RuntimeUID: 1000, RuntimeGID: 1000,
					RuntimeHome: "/home/dev", SSHPublicKey: test.publicKey})
			require.Error(t, err)
		})
	}
}

func TestProvisionStartsInitialAppServerWithRuntimeCredential(t *testing.T) {
	runner := &recordingCommandRunner{resultFor: map[string]string{
		`{{.Id}}`:                     "sha256:development",
		`index .Config.Labels`:        "1",
		`{{.Config.User}}`:            "developer",
		`TYRS_RUNTIME_USER=developer`: "developer:1000:1000:/home/developer",
	}}
	manager := &Manager{dockerBin: "docker", dockerHost: "inherit", runner: runner,
		developmentRuntimeDir: t.TempDir(), developmentRuntimeHostDir: "/host/runtime"}
	item := workspace{Environment: environment{
		ID: uuid.New(), Status: "pending", ImageRef: "development-image",
		ContainerName: "development", DataVolume: "development-data",
		HomeVolume: "development-home", Network: "development-network",
	}}

	err := manager.provision(context.Background(), &item, "git-credential",
		[]string{"TYRS_HAND_MODEL_API_KEY=managed-secret"})
	require.NoError(t, err)
	require.True(t, runner.contains("--env TYRS_HAND_MODEL_API_KEY=managed-secret"))
}

func TestRunRemoteDevelopmentOperations(t *testing.T) {
	runner := &recordingCommandRunner{resultFor: map[string]string{
		`index .Config.Labels`:                   "1",
		`{{.Config.User}}`:                       "developer",
		`{{.Id}}`:                                "sha256:development",
		`TYRS_RUNTIME_USER=developer`:            "developer:1000:1000:/home/developer",
		`test -S /run/tyrs-hand/app-server.sock`: "",
	}}
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
	} {
		require.True(t, runner.contains(expected...))
	}

	require.NoError(t, manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "rebase", EnvironmentID: uuid.New(), ContainerName: "dev-container",
		ImageRef: "image-ref", DataVolume: "data-volume", HomeVolume: "home-volume",
		Network: "dev-network", RuntimeUser: "developer", RuntimeUID: 1000,
		RuntimeGID: 1000, RuntimeHome: "/home/developer",
	}))
	require.Error(t, manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "start", ContainerName: "dev-container",
	}))
	require.Error(t, manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "stop", ContainerName: "dev-container",
	}))
}

func TestReconfigureRemoteEnvironmentKeepsContainerRunningAndSecuresSSH(t *testing.T) {
	runner := &recordingCommandRunner{resultFor: map[string]string{
		`NetworkSettings.Ports`: "",
	}}
	runtimeDir := t.TempDir()
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit", runner: runner,
		developmentRuntimeDir: runtimeDir, developmentRuntimeHostDir: "/host/runtime",
		sshEnabled: true, sshAgentDir: "/run/tyrs-hand-ssh-agent",
		sshAgentHostDir: "/host/ssh-agent"}
	environmentID := uuid.New()
	err := manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "reconfigure", EnvironmentID: environmentID,
		ContainerName: "dev-container", ImageRef: "dev-image", DataVolume: "dev-data",
		HomeVolume: "dev-home", Network: "dev-network", RuntimeUser: "agent",
		RuntimeUID: 1000, RuntimeGID: 1000, RuntimeHome: "/home/agent",
		SSHPort: 2222, SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest",
		SSHConfigRevision:  3,
		ProcessEnvironment: []string{"TYRS_TEST_RUNTIME=value"},
	})
	require.NoError(t, err)
	require.True(t, runner.contains("docker create"))
	require.True(t, runner.contains("--restart unless-stopped"))
	require.True(t, runner.contains("--publish 2222:22"))
	require.True(t, runner.contains("type=bind,source=/host/ssh-agent,target=/run/tyrs-hand-ssh-agent"))
	require.True(t, runner.contains("--env SSH_AUTH_SOCK=/run/tyrs-hand-ssh-agent/current.sock"))
	require.True(t, runner.contains("--env TYRS_TEST_RUNTIME=value"))
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
	require.True(t, runner.contains(`shell_environment_policy.exclude=["TYRS_HAND_MODEL_API_KEY"]`))
	require.True(t, runner.contains("allow_login_shell=false"))
	require.True(t, runner.contains(`openai_base_url="https://chatgpt.com/backend-api/codex"`))
}

func TestReconfigureRemoteUpdatesSSHKeyInPlaceWhenPortIsUnchanged(t *testing.T) {
	runner := &recordingCommandRunner{resultFor: map[string]string{
		`NetworkSettings.Ports`: "2222",
	}}
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: runner}
	err := manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "reconfigure", EnvironmentID: uuid.New(), ContainerName: "dev-container",
		ImageRef: "dev-image", DataVolume: "dev-data", HomeVolume: "dev-home",
		Network: "dev-network", RuntimeUser: "developer", RuntimeUID: 1000,
		RuntimeGID: 1000, RuntimeHome: "/home/developer", SSHPort: 2222,
		SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIUpdated",
	})
	require.NoError(t, err)
	require.True(t, runner.contains("authorized_keys"))
	require.True(t, runner.contains("sshd -D -e"))
	require.False(t, runner.contains("docker create"))
	require.False(t, runner.contains("app-server.pid"))
}

func TestCodexStateAndRollbackUsePersistentHomeSelection(t *testing.T) {
	runner := &recordingCommandRunner{resultFor: map[string]string{
		"codex --version":            "codex-cli 0.146.0",
		"tyrs-hand-dev codex status": "0.146.0",
	}}
	manager := &Manager{dockerBin: "docker", dockerHost: "inherit", runner: runner}
	runtime := Runtime{Container: "dev-container", UID: 1000, GID: 1000,
		Home: "/home/developer"}
	version, override, restart, err := manager.CodexState(context.Background(), runtime)
	require.NoError(t, err)
	require.Equal(t, "codex-cli 0.146.0", version)
	require.True(t, override)
	require.True(t, restart)

	failing := &recordingCommandRunner{failContains: "codex rollback"}
	manager.runner = failing
	require.Error(t, manager.RollbackUserCodex(context.Background(), runtime))
	require.NoError(t, manager.ResetUserCodex(context.Background(), runtime))
	require.True(t, failing.contains("tyrs-hand-dev codex rollback"))
	require.True(t, failing.contains("tyrs-hand-dev codex reset"))
}

func TestRebaseCandidateFailureRestoresOldContainer(t *testing.T) {
	runner := &recordingCommandRunner{failContains: "create --name", resultFor: map[string]string{
		`index .Config.Labels`:        "1",
		`{{.Config.User}}`:            "developer",
		`{{.Id}}`:                     "sha256:development",
		`TYRS_RUNTIME_USER=developer`: "developer:1000:1000:/home/developer",
	}}
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: runner, developmentRuntimeDir: t.TempDir(),
		developmentRuntimeHostDir: "/host/runtime"}
	err := manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "rebase", EnvironmentID: uuid.New(), ContainerName: "dev-container",
		ImageRef: "dev-image", DataVolume: "dev-data", HomeVolume: "dev-home",
		Network: "dev-network", RuntimeUser: "developer", RuntimeUID: 1000,
		RuntimeGID: 1000, RuntimeHome: "/home/developer",
	})
	require.ErrorContains(t, err, "创建重配开发容器")
	require.True(t, runner.contains("stop --time 10 dev-container"))
	require.True(t, runner.contains("start dev-container"))
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
	}, "credential", nil)
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
	}, "credential", nil)
	require.NoError(t, err)
	require.Equal(t, runtime.CodexHome, second.CodexHome)
}

func TestEnsureRemoteRequiresDevelopmentContainers(t *testing.T) {
	manager := &Manager{}
	_, _, err := manager.EnsureRemote(context.Background(), RemoteSpec{}, "", nil)
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
	require.True(t, runner.contains(`shell_environment_policy.inherit="core"`))
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
