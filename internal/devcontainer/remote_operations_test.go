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
	"github.com/slovx2/tyrs-hand/internal/codex"
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
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit", runner: runner,
		developmentRuntimeDir: t.TempDir(), developmentRuntimeHostDir: "/host/runtime"}
	appServerConfig := codex.ManagedAppServerConfig{ModelProvider: codex.ManagedModelProvider{
		ID: "tyrs-hand-provider", Name: "Tyrs Hand Provider",
		BaseURL: "https://api.example.com/v1", WireAPI: "responses",
		EnvKey: "TYRS_HAND_MODEL_API_KEY", RequiresOpenAIAuth: false,
	}}
	operation := RemoteOperation{
		EnvironmentID: uuid.New(), ImageRef: "development-image",
		ContainerName: "development", DataVolume: "development-data",
		HomeVolume: "development-home", Network: "development-network",
		AppServerConfig:    appServerConfig,
		ProcessEnvironment: []string{"TYRS_HAND_MODEL_API_KEY=managed-secret"},
	}

	runtime, err := manager.ProvisionRemoteEnvironment(context.Background(), &operation)
	require.NoError(t, err)
	require.True(t, runner.contains("--env TYRS_HAND_MODEL_API_KEY=managed-secret"))
	require.True(t, runner.contains(`model_provider="tyrs-hand-provider"`))
	require.True(t, runner.contains(
		`model_providers.tyrs-hand-provider.base_url="https://api.example.com/v1"`))
	require.True(t, runner.contains(
		"model_providers.tyrs-hand-provider.requires_openai_auth=false"))
	require.Equal(t, appServerConfig, runtime.AppServerConfig)
	require.Equal(t, operation.ProcessEnvironment, runtime.ProcessEnvironment)
}

func TestScanRemoteProjectsClassifiesDirectoriesAndRedactsRemote(t *testing.T) {
	runner := &recordingCommandRunner{resultFor: map[string]string{
		"find /var/lib/tyrs-hand/workspaces": "atlas\x00notes\x00",
		"workspaces/atlas/.git":              "1",
		"workspaces/notes/.git":              "0",
		"rev-parse --show-toplevel":          "/var/lib/tyrs-hand/workspaces/atlas",
		"status --porcelain=v1":              " M README.md",
		"symbolic-ref --short":               "main",
		"rev-parse --verify HEAD":            "0123456789abcdef",
		"remote get-url origin":              "https://token@example.invalid/owner/atlas.git?access_token=secret",
	}}
	manager := &Manager{dockerBin: "docker", dockerHost: "inherit", runner: runner}
	projects, err := manager.ScanRemoteProjects(context.Background(),
		workerprotocol.EnvironmentManifest{
			ContainerName: "development", RuntimeUID: 1000, RuntimeGID: 1000,
			RuntimeHome: "/home/developer",
		})
	require.NoError(t, err)
	require.Equal(t, []workerprotocol.DevelopmentProjectSnapshot{
		{
			Name: "atlas", RelativePath: "workspaces/atlas", ProjectKind: "git",
			Branch: "main", HeadSHA: "0123456789abcdef", Dirty: true,
			RemoteURL: "https://example.invalid/owner/atlas.git",
		},
		{Name: "notes", RelativePath: "workspaces/notes", ProjectKind: "directory"},
	}, projects)
	require.True(t, runner.contains("-type d ! -name .*"))
	require.True(t, runner.contains("-printf %f\\0"))
}

func TestScanRemoteProjectsReportsCommandFailures(t *testing.T) {
	tests := []struct {
		name         string
		failContains string
	}{
		{name: "启动容器", failContains: "start development"},
		{name: "创建根目录", failContains: "mkdir -p /var/lib/tyrs-hand/workspaces"},
		{name: "修正根目录权限", failContains: "chown 1000:1000"},
		{name: "扫描一级目录", failContains: "find /var/lib/tyrs-hand/workspaces"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := &Manager{dockerBin: "docker", dockerHost: "inherit",
				runner: &recordingCommandRunner{failContains: test.failContains}}
			_, err := manager.ScanRemoteProjects(context.Background(),
				workerprotocol.EnvironmentManifest{
					ContainerName: "development", RuntimeUID: 1000, RuntimeGID: 1000,
					RuntimeHome: "/home/developer",
				})
			require.Error(t, err)
		})
	}
}

func TestScanRemoteProjectsReportsInvalidGitMetadata(t *testing.T) {
	tests := []struct {
		name         string
		failContains string
		resultFor    map[string]string
		errorText    string
	}{
		{
			name: "Git 标记检测失败", resultFor: map[string]string{
				"find /var/lib/tyrs-hand/workspaces": "atlas\x00.hidden\x00nested/path\x00",
				"workspaces/atlas/.git":              "unexpected",
			}, errorText: "容器路径检测返回无效结果",
		},
		{
			name: "Git 根目录读取失败", failContains: "rev-parse --show-toplevel",
			resultFor: map[string]string{
				"find /var/lib/tyrs-hand/workspaces": "atlas\x00",
				"workspaces/atlas/.git":              "1",
			}, errorText: "读取项目",
		},
		{
			name: "Git 根目录不匹配", resultFor: map[string]string{
				"find /var/lib/tyrs-hand/workspaces": "atlas\x00",
				"workspaces/atlas/.git":              "1",
				"rev-parse --show-toplevel":          "/var/lib/tyrs-hand/workspaces/other",
			}, errorText: "Git 根目录不匹配",
		},
		{
			name: "Git 状态读取失败", failContains: "status --porcelain=v1",
			resultFor: map[string]string{
				"find /var/lib/tyrs-hand/workspaces": "atlas\x00",
				"workspaces/atlas/.git":              "1",
				"rev-parse --show-toplevel":          "/var/lib/tyrs-hand/workspaces/atlas",
			}, errorText: "读取项目",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := &Manager{dockerBin: "docker", dockerHost: "inherit",
				runner: &recordingCommandRunner{
					failContains: test.failContains, resultFor: test.resultFor,
				}}
			_, err := manager.ScanRemoteProjects(context.Background(),
				workerprotocol.EnvironmentManifest{
					ContainerName: "development", RuntimeUID: 1000, RuntimeGID: 1000,
					RuntimeHome: "/home/developer",
				})
			require.ErrorContains(t, err, test.errorText)
		})
	}
}

func TestRelocateRemoteProjectIsAtomicAndIdempotent(t *testing.T) {
	tests := []struct {
		name         string
		sourceExists string
		targetExists string
		wantMove     bool
		wantError    string
	}{
		{name: "移动", sourceExists: "1", targetExists: "0", wantMove: true},
		{name: "完成后重试", sourceExists: "0", targetExists: "1"},
		{name: "目标冲突", sourceExists: "1", targetExists: "1",
			wantError: "项目迁移目标已存在"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingCommandRunner{resultFor: map[string]string{
				"workspaces/source": test.sourceExists,
				"workspaces/target": test.targetExists,
			}}
			manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
				runner: runner}
			err := manager.RunRemoteOperation(context.Background(), RemoteOperation{
				Operation: "relocate_project", ContainerName: "development",
				Workspace: "workspaces/source", TargetWorkspace: "workspaces/target",
			})
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, test.wantMove, runner.contains(
				"mv -- /var/lib/tyrs-hand/workspaces/source /var/lib/tyrs-hand/workspaces/target"))
		})
	}
}

func TestRelocateRemoteProjectReportsCommandFailures(t *testing.T) {
	tests := []struct {
		name         string
		failContains string
		resultFor    map[string]string
		errorText    string
	}{
		{
			name: "源目录检测失败", failContains: "workspaces/source",
			errorText: "injected command failure",
		},
		{
			name: "目标目录检测结果无效", resultFor: map[string]string{
				"workspaces/source": "1",
				"workspaces/target": "unexpected",
			}, errorText: "容器路径检测返回无效结果",
		},
		{
			name: "创建目标父目录失败", failContains: "mkdir -p",
			resultFor: map[string]string{
				"workspaces/source": "1",
				"workspaces/target": "0",
			}, errorText: "injected command failure",
		},
		{
			name: "原子移动失败", failContains: "mv --",
			resultFor: map[string]string{
				"workspaces/source": "1",
				"workspaces/target": "0",
			}, errorText: "injected command failure",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
				runner: &recordingCommandRunner{
					failContains: test.failContains, resultFor: test.resultFor,
				}}
			err := manager.RunRemoteOperation(context.Background(), RemoteOperation{
				Operation: "relocate_project", ContainerName: "development",
				Workspace: "workspaces/source", TargetWorkspace: "workspaces/target",
			})
			require.ErrorContains(t, err, test.errorText)
		})
	}
}

func TestRelocateRemoteProjectAcceptsIdenticalPath(t *testing.T) {
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: &recordingCommandRunner{}}
	require.NoError(t, manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "relocate_project", ContainerName: "development",
		Workspace: "workspaces/atlas", TargetWorkspace: "workspaces/atlas",
	}))
}

func TestRelocateRemoteProjectRejectsInvalidPaths(t *testing.T) {
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: &recordingCommandRunner{}}
	require.Error(t, manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "relocate_project", ContainerName: "development",
		Workspace: "", TargetWorkspace: "workspaces/atlas",
	}))
	require.Error(t, manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "relocate_project", ContainerName: "development",
		Workspace: "workspaces/atlas", TargetWorkspace: "../atlas",
	}))
}

func TestRedactGitRemoteKeepsSSHAndRemovesHTTPSecrets(t *testing.T) {
	require.Equal(t, "git@example.invalid:owner/repo.git",
		RedactGitRemote("git@example.invalid:owner/repo.git"))
	require.Equal(t, "https://example.invalid/owner/repo.git",
		RedactGitRemote("https://user:password@example.invalid/owner/repo.git?token=secret"))
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

func TestRemoteResourceCleanupHandlesMissingAndCommandFailures(t *testing.T) {
	missingRunner := &recordingCommandRunner{failContains: "container inspect"}
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: missingRunner}
	require.NoError(t, manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "delete_forum", ContainerName: "missing-container",
		Workspace: "workspaces/atlas",
	}))
	require.NoError(t, manager.removeDockerResource(context.Background(), "container", ""))
	require.NoError(t, manager.removeDockerResource(context.Background(),
		"container", "missing-container"))
	require.False(t, manager.dockerResourceExists(context.Background(), "container", ""))

	startFailure := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: &recordingCommandRunner{failContains: "start development"}}
	require.Error(t, startFailure.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "delete_forum", ContainerName: "development",
		Workspace: "workspaces/atlas",
	}))

	removeFailure := &Manager{dockerBin: "docker", dockerHost: "inherit",
		runner: &recordingCommandRunner{failContains: "container rm"}}
	require.Error(t, removeFailure.removeDockerResource(context.Background(),
		"container", "development"))
}

func TestRunRemoteDevelopmentOperationReportsMaintenanceFailures(t *testing.T) {
	imageFailure := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: &recordingCommandRunner{failContains: "image inspect"}}
	require.Error(t, imageFailure.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "rebase", ImageRef: "development-image",
	}))

	incompatible := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: &recordingCommandRunner{resultFor: map[string]string{
			`index .Config.Labels`:        "1",
			`{{.Config.User}}`:            "developer",
			`TYRS_RUNTIME_USER=developer`: "developer:1000:1000:/home/developer",
		}}}
	require.ErrorContains(t, incompatible.RunRemoteOperation(context.Background(),
		RemoteOperation{
			Operation: "rebase", ImageRef: "development-image",
			RuntimeUID: 2000, RuntimeGID: 2000, RuntimeHome: "/home/other",
		}), "不兼容")

	for _, failure := range []string{
		"container rm", "volume rm data-volume", "network rm development-network",
	} {
		manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
			runner: &recordingCommandRunner{failContains: failure}}
		require.Error(t, manager.RunRemoteOperation(context.Background(), RemoteOperation{
			Operation: "delete_environment", ContainerName: "development",
			DataVolume: "data-volume", HomeVolume: "home-volume",
			Network: "development-network",
		}))
	}
}

func TestReconfigureRemoteEnvironmentKeepsContainerRunningAndSecuresSSH(t *testing.T) {
	runner := &recordingCommandRunner{resultFor: map[string]string{
		`NetworkSettings.Ports`: "",
	}}
	runtimeDir := t.TempDir()
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit", runner: runner,
		developmentRuntimeDir: runtimeDir, developmentRuntimeHostDir: "/host/runtime",
		sshEnabled: true, sshAgentDir: "/run/tyrs-hand-ssh-agent",
		sshAgentHostDir: "/host/ssh-agent",
		browserEnabled:  true, browserFilesRoot: "/run/tyrs-hand-browser-files",
		browserFilesHostRoot:    "/host/browser-files",
		browserServicesRoot:     filepath.Join(runtimeDir, "browser-services"),
		browserServicesHostRoot: "/host/browser-services"}
	environmentID := uuid.New()
	err := manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "reconfigure", EnvironmentID: environmentID,
		ContainerName: "dev-container", ImageRef: "dev-image", DataVolume: "dev-data",
		HomeVolume: "dev-home", Network: "dev-network", RuntimeUser: "agent",
		RuntimeUID: 1000, RuntimeGID: 1000, RuntimeHome: "/home/agent",
		SSHPort: 2222, SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest",
		SSHConfigRevision: 3,
		AppServerConfig: codex.ManagedAppServerConfig{
			ModelProvider: codex.ManagedModelProvider{
				ID: "tyrs-hand-provider", Name: "Tyrs Hand Provider",
				BaseURL: "https://api.example.com/v1", WireAPI: "responses",
				EnvKey: "TYRS_HAND_MODEL_API_KEY", RequiresOpenAIAuth: true,
			},
		},
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
	require.True(t, runner.contains("type=bind,source=/host/browser-services/"+
		environmentID.String()+",target=/run/tyrs-hand-browser-services"))
	require.True(t, runner.contains("PasswordAuthentication no"))
	require.True(t, runner.contains("PermitRootLogin no"))
	require.True(t, runner.contains("AllowTcpForwarding local"))
	require.True(t, runner.contains("PermitOpen 127.0.0.1:*"))
	require.True(t, runner.contains("GatewayPorts no"))
	require.False(t, runner.contains("DisableForwarding yes"))
	require.True(t, runner.contains("AuthenticationMethods publickey"))
	require.True(t, runner.contains("chown \"$TYRS_OWNER\" /run/tyrs-hand"))
	require.True(t, runner.contains("chmod 0700 /run/tyrs-hand"))
	require.True(t, runner.contains("kill -KILL \"$TYRS_APP_SERVER_PID\""))
	require.True(t, runner.contains("grep -Fxq \"unix:///run/tyrs-hand/app-server.sock\""))
	require.True(t, runner.contains("chmod 0777 /run/tyrs-hand"))
	require.True(t, runner.contains("chmod 0666 /run/tyrs-hand/app-server.sock"))
	require.True(t, runner.contains("docker exec --detach --user 0:0"))
	require.True(t, runner.contains("sshd -D -e"))
	require.True(t, runner.contains("tyrs-hand-dev service proxy"))
	require.True(t, runner.contains(
		`shell_environment_policy.exclude=["TYRS_HAND_MODEL_API_KEY","TYRS_BROWSER_MCP_TOKEN"]`))
	require.True(t, runner.contains("allow_login_shell=false"))
	require.True(t, runner.contains(`openai_base_url="https://chatgpt.com/backend-api/codex"`))
	require.True(t, runner.contains(`model_provider="tyrs-hand-provider"`))
	require.True(t, runner.contains(
		`model_providers.tyrs-hand-provider.base_url="https://api.example.com/v1"`))
}

func TestPrepareBrowserServicesDirectoryPreservesExistingRuntimeOwnership(t *testing.T) {
	root := t.TempDir()
	environmentID := uuid.New()
	directory := filepath.Join(root, environmentID.String())
	require.NoError(t, os.Mkdir(directory, 0o750))
	require.NoError(t, os.Chmod(directory, 0o750))

	manager := &Manager{browserEnabled: true, browserServicesRoot: root}
	require.NoError(t, manager.prepareBrowserServicesDirectory(environmentID))
	info, err := os.Stat(directory)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o750), info.Mode().Perm())
}

func TestHostDockerUsesHostNetworkSocketAndAssignedSSHPort(t *testing.T) {
	runner := &recordingCommandRunner{resultFor: map[string]string{
		`HostConfig.NetworkMode`: "host",
	}, failContains: "container inspect dev-container"}
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: runner, hostDocker: true, dockerSocketGID: 984,
		developmentRuntimeDir: t.TempDir(), developmentRuntimeHostDir: "/host/runtime"}
	err := manager.RunRemoteOperation(context.Background(), RemoteOperation{
		Operation: "reconfigure", EnvironmentID: uuid.New(), ContainerName: "dev-container",
		ImageRef: "dev-image", DataVolume: "dev-data", HomeVolume: "dev-home",
		Network: "dev-network", RuntimeUser: "developer", RuntimeUID: 1000,
		RuntimeGID: 1000, RuntimeHome: "/home/developer", SSHPort: 22222,
		SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest",
	})
	require.NoError(t, err)
	require.True(t, runner.contains("--network host"))
	require.True(t, runner.contains(
		"type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock"))
	require.True(t, runner.contains("--group-add 984"))
	require.True(t, runner.contains("TYRS_DOCKER_GID=984"))
	require.True(t, runner.contains("TYRS_RUNTIME_USER=developer"))
	require.True(t, runner.contains("usermod --append --groups"))
	require.True(t, runner.contains("com.tyrs-hand.ssh-port=22222"))
	require.True(t, runner.contains("Port 22222"))
	require.False(t, runner.contains("--publish"))
	require.False(t, runner.contains("host.docker.internal:host-gateway"))
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
		WorkspaceKind: "git",
		Repository:    "owner/repo", CloneURL: "https://example.invalid/owner/repo.git",
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
	require.True(t, runner.contains("kill -KILL \"$TYRS_APP_SERVER_PID\""))
	require.True(t, runner.contains("grep -Fxq \"unix:///run/tyrs-hand/app-server.sock\""))

	_, err = (&Manager{}).PrepareRemoteRuntime(context.Background(), manifest)
	require.ErrorContains(t, err, "未启用")
	manifest.RuntimeUID = 0
	_, err = manager.PrepareRemoteRuntime(context.Background(), manifest)
	require.ErrorContains(t, err, "Manifest")
}

func TestPrepareRemoteRuntimeRefreshesSSHConfiguration(t *testing.T) {
	agentDir := t.TempDir()
	sshConfig := filepath.Join(agentDir, "ssh_config")
	require.NoError(t, os.WriteFile(sshConfig, []byte("Host server\n"), 0o644))
	runner := &recordingCommandRunner{}
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: runner, developmentRuntimeDir: t.TempDir(), sshEnabled: true,
		sshAgentDir: agentDir}
	manifest := workerprotocol.EnvironmentManifest{
		EnvironmentID: uuid.New(), ContainerName: "dev-container", RuntimeUser: "agent",
		RuntimeUID: 1000, RuntimeGID: 1000, RuntimeHome: "/home/agent",
	}

	_, err := manager.PrepareRemoteRuntime(context.Background(), manifest)
	require.NoError(t, err)
	require.True(t, runner.contains("docker start dev-container"))
	require.True(t, runner.contains("docker cp "+sshConfig+" dev-container:"+
		managedSSHConfigPath+".tmp"))
	require.False(t, runner.contains("sshd -D -e"))
	require.False(t, runner.contains("tyrs-hand-app-server"))
}

func TestPrepareRemoteRuntimeKeepsCurrentSSHConfiguration(t *testing.T) {
	agentDir := t.TempDir()
	content := []byte("Host server\n")
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "ssh_config"), content, 0o644))
	digest := sha256.Sum256(content)
	runner := &recordingCommandRunner{resultFor: map[string]string{
		"cat " + managedSSHConfigPath + ".sha256": hex.EncodeToString(digest[:]),
	}}
	manager := &Manager{enabled: true, dockerBin: "docker", dockerHost: "inherit",
		runner: runner, developmentRuntimeDir: t.TempDir(), sshEnabled: true,
		sshAgentDir: agentDir}
	manifest := workerprotocol.EnvironmentManifest{
		EnvironmentID: uuid.New(), ContainerName: "dev-container", RuntimeUser: "agent",
		RuntimeUID: 1000, RuntimeGID: 1000, RuntimeHome: "/home/agent",
	}

	_, err := manager.PrepareRemoteRuntime(context.Background(), manifest)
	require.NoError(t, err)
	require.False(t, runner.contains("docker cp"))
}
