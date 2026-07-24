package devcontainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestRefreshRemoteRuntimeWhenWorkerBinariesOrArgumentsChange(t *testing.T) {
	root := t.TempDir()
	codexBin := filepath.Join(root, "codex-real")
	proxyBin := filepath.Join(root, "codex")
	require.NoError(t, os.WriteFile(codexBin, []byte("codex-v2"), 0o755))
	require.NoError(t, os.WriteFile(proxyBin, []byte("proxy-v2"), 0o755))
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		codexBin: codexBin, codexProxyBin: proxyBin}
	signature, err := manager.desiredRuntimeSignature()
	require.NoError(t, err)
	legacyHash := sha256.New()
	for _, content := range []string{"codex-v2", "proxy-v2"} {
		_, err = legacyHash.Write([]byte(content))
		require.NoError(t, err)
		_, err = legacyHash.Write([]byte{0})
		require.NoError(t, err)
	}
	legacySignature := hex.EncodeToString(legacyHash.Sum(nil))
	require.NotEqual(t, legacySignature, signature)

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
		"cat " + runtimeSignaturePath: legacySignature,
	}}
	manager.runner = staleRunner
	changed, err = manager.RefreshRemoteRuntime(context.Background(), runtime)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, staleRunner.contains("app-server.pid"))
	require.True(t, staleRunner.contains("docker cp --follow-link "+codexBin))
	require.True(t, staleRunner.contains("TYRS_RUNTIME_SIGNATURE="+signature))
}

func TestRefreshRemoteRuntimeReportsRefreshFailures(t *testing.T) {
	root := t.TempDir()
	codexBin := filepath.Join(root, "codex-real")
	proxyBin := filepath.Join(root, "codex")
	require.NoError(t, os.WriteFile(codexBin, []byte("codex"), 0o755))
	require.NoError(t, os.WriteFile(proxyBin, []byte("proxy"), 0o755))
	runtime := Runtime{Container: "dev-container", UID: 1000, GID: 1000, Home: "/home/dev"}

	t.Run("计算签名", func(t *testing.T) {
		manager := &Manager{dockerBin: "docker", dockerHost: "inherit",
			codexBin: filepath.Join(root, "missing"), codexProxyBin: proxyBin,
			runner: &recordingCommandRunner{}}
		_, err := manager.RefreshRemoteRuntime(context.Background(), runtime)
		require.ErrorContains(t, err, "计算 Codex 运行时签名")
	})

	t.Run("停止旧进程", func(t *testing.T) {
		runner := &recordingCommandRunner{failContains: "app-server.pid"}
		manager := &Manager{dockerBin: "docker", dockerHost: "inherit",
			codexBin: codexBin, codexProxyBin: proxyBin, runner: runner}
		_, err := manager.RefreshRemoteRuntime(context.Background(), runtime)
		require.ErrorContains(t, err, "停止旧 Codex app-server")
	})

	t.Run("安装运行时", func(t *testing.T) {
		temporaryCodex := filepath.Join(t.TempDir(), "codex-real")
		require.NoError(t, os.WriteFile(temporaryCodex, []byte("codex"), 0o755))
		manager := &Manager{dockerBin: "docker", dockerHost: "inherit",
			codexBin: temporaryCodex, codexProxyBin: proxyBin,
			runner: &recordingCommandRunner{}}
		_, err := manager.desiredRuntimeSignature()
		require.NoError(t, err)
		require.NoError(t, os.Remove(temporaryCodex))
		_, err = manager.RefreshRemoteRuntime(context.Background(), runtime)
		require.ErrorContains(t, err, "刷新 Codex 运行时")
	})
}

func TestInstallRuntimeReportsIncompleteInstallation(t *testing.T) {
	tests := []struct {
		name         string
		failContains string
		remove       string
		replyHook    bool
	}{
		{name: "Codex 不存在", remove: "codex"},
		{name: "Proxy 不存在", remove: "proxy"},
		{name: "无法创建目录", failContains: "mkdir -p /opt/tyrs-hand/bin"},
		{name: "无法复制 Codex", failContains: "cp --follow-link"},
		{name: "无法复制 Proxy", failContains: "/opt/tyrs-hand/bin/codex"},
		{name: "无法复制 Reply Hook", failContains: "tyrs-hand-reply-hook", replyHook: true},
		{name: "无法写入签名", failContains: "TYRS_RUNTIME_SIGNATURE="},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			codexBin := filepath.Join(root, "codex-real")
			proxyBin := filepath.Join(root, "codex")
			replyHook := filepath.Join(root, "reply-hook")
			require.NoError(t, os.WriteFile(codexBin, []byte("codex"), 0o755))
			require.NoError(t, os.WriteFile(proxyBin, []byte("proxy"), 0o755))
			if test.replyHook {
				require.NoError(t, os.WriteFile(replyHook, []byte("hook"), 0o755))
			}
			runner := &recordingCommandRunner{failContains: test.failContains}
			manager := &Manager{dockerBin: "docker", dockerHost: "inherit", runner: runner,
				codexBin: codexBin, codexProxyBin: proxyBin, replyHook: replyHook}
			_, err := manager.desiredRuntimeSignature()
			require.NoError(t, err)
			switch test.remove {
			case "codex":
				require.NoError(t, os.Remove(codexBin))
			case "proxy":
				require.NoError(t, os.Remove(proxyBin))
			}
			require.Error(t, manager.installRuntime(context.Background(), "development",
				1000, 1000, "/home/dev"))
		})
	}
}

func TestRefreshRemoteRuntimeReportsSSHConfigurationFailure(t *testing.T) {
	root := t.TempDir()
	codexBin := filepath.Join(root, "codex-real")
	proxyBin := filepath.Join(root, "codex")
	require.NoError(t, os.WriteFile(codexBin, []byte("codex"), 0o755))
	require.NoError(t, os.WriteFile(proxyBin, []byte("proxy"), 0o755))
	manager := &Manager{dockerBin: "docker", dockerHost: "inherit",
		codexBin: codexBin, codexProxyBin: proxyBin, sshEnabled: true,
		sshAgentDir: filepath.Join(root, "missing-agent"),
		runner:      &recordingCommandRunner{failContains: "mkdir -p /etc/ssh"}}

	_, err := manager.RefreshRemoteRuntime(context.Background(), Runtime{
		Container: "development", UID: 1000, GID: 1000, Home: "/home/dev",
	})
	require.ErrorContains(t, err, "刷新 SSH 配置")
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

func TestInstallRuntimeCopiesRootOwnedSSHConfigAndRemovesLegacyInclude(t *testing.T) {
	root := t.TempDir()
	codexBin := filepath.Join(root, "codex-real")
	proxyBin := filepath.Join(root, "codex")
	agentDir := filepath.Join(root, "ssh-agent")
	require.NoError(t, os.WriteFile(codexBin, []byte("codex"), 0o755))
	require.NoError(t, os.WriteFile(proxyBin, []byte("proxy"), 0o755))
	require.NoError(t, os.Mkdir(agentDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "ssh_config"),
		[]byte("Host *\n"), 0o644))
	runner := &recordingCommandRunner{}
	manager := &Manager{dockerBin: "docker", dockerHost: "inherit", runner: runner,
		codexBin: codexBin, codexProxyBin: proxyBin, sshEnabled: true,
		sshAgentDir: agentDir}

	require.NoError(t, manager.installRuntime(context.Background(), "development",
		1000, 1000, "/home/vscode"))
	require.True(t, runner.contains("docker cp "+filepath.Join(agentDir, "ssh_config")+" "+
		"development:/etc/ssh/ssh_config.d/99-tyrs-hand.conf.tmp"))
	require.True(t, runner.contains(`TYRS_INCLUDE=Include /etc/ssh/ssh_config.d/99-tyrs-hand.conf`))
	require.True(t, runner.contains(`TYRS_LEGACY_INCLUDE=Include `+agentDir+`/ssh_config`))
	require.True(t, runner.contains(`system_config="/etc/ssh/ssh_config"`))
	require.True(t, runner.contains(`config="$TYRS_HOME/.ssh/config"`))
	require.True(t, runner.contains(`TYRS_PROFILE=/etc/profile.d/tyrs-hand-ssh-agent.sh`))
}

func TestRefreshRemoteRuntimeKeepsCurrentSSHConfig(t *testing.T) {
	root := t.TempDir()
	codexBin := filepath.Join(root, "codex-real")
	proxyBin := filepath.Join(root, "codex")
	agentDir := filepath.Join(root, "ssh-agent")
	sshConfig := []byte("Host *\n")
	require.NoError(t, os.WriteFile(codexBin, []byte("codex"), 0o755))
	require.NoError(t, os.WriteFile(proxyBin, []byte("proxy"), 0o755))
	require.NoError(t, os.Mkdir(agentDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "ssh_config"), sshConfig, 0o644))
	manager := &Manager{dockerBin: "docker", dockerHost: "inherit",
		codexBin: codexBin, codexProxyBin: proxyBin, sshEnabled: true,
		sshAgentDir: agentDir}
	signature, err := manager.desiredRuntimeSignature()
	require.NoError(t, err)
	checksum := sha256.Sum256(sshConfig)
	runner := &recordingCommandRunner{resultFor: map[string]string{
		"cat " + managedSSHConfigPath + ".sha256": hex.EncodeToString(checksum[:]),
		"cat " + runtimeSignaturePath:             signature,
	}}
	manager.runner = runner

	changed, err := manager.RefreshRemoteRuntime(context.Background(), Runtime{
		Container: "development", UID: 1000, GID: 1000, Home: "/home/vscode",
	})
	require.NoError(t, err)
	require.False(t, changed)
	require.False(t, runner.contains("docker cp"))
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
		codexBin: codexBin, codexProxyBin: proxyBin, sshEnabled: true,
		sshAgentDir: "/run/tyrs-hand-ssh-agent", sshAgentHostDir: "/host/ssh-agent"}
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
