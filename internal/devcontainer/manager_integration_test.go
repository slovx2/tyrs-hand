//go:build integration

package devcontainer

import (
	"bufio"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	_ "github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexrelay"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

func TestUserEnvironmentSharesHomeAndKeepsIndependentRepositoryClones(t *testing.T) {
	ctx := context.Background()
	db := developmentDatabase(t)
	require.NoError(t, database.Migrate(ctx, db))
	root := t.TempDir()
	firstRepository := createGitRepository(t, filepath.Join(root, "first-repo"), "one.txt", "one")
	secondRepository := createGitRepository(t, filepath.Join(root, "second-repo"), "two.txt", "two")
	runtimeRoot, err := os.MkdirTemp("/tmp", "tyrs-dev-runtime-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(runtimeRoot)) })

	t.Setenv("TYRS_HAND_DOCKER_REAL_BIN", "docker")
	t.Setenv("TYRS_HAND_DOCKER_HOST", "inherit")
	manager, err := NewManager(config.Config{
		WorkerDataRoot: root, WorkerRole: "discord", EnableDevelopmentContainers: true,
		DevelopmentRuntimeDir: runtimeRoot, DevelopmentRuntimeHostDir: runtimeRoot,
	}, db, zap.NewNop())
	require.NoError(t, err)
	imageRef := buildDevelopmentTestImage(t, manager, root, "dev", 1001, "initial")
	environmentID, firstForumID, secondForumID := seedDevelopmentEnvironment(t, db,
		firstRepository, secondRepository, imageRef)
	t.Cleanup(func() { cleanupDevelopmentResources(manager, environmentID) })

	conversationOne := mustUUID(t, "10000000-0000-0000-0000-000000000001")
	first, err := manager.Ensure(ctx, environmentID, firstForumID, conversationOne, "")
	require.NoError(t, err)
	appServerPID := strings.TrimSpace(dockerRead(t, manager, first,
		"/run/tyrs-hand/app-server.pid"))
	appServerExecutable, err := manager.docker(ctx, "exec", first.Container,
		"readlink", "/proc/"+appServerPID+"/exe")
	require.NoError(t, err)
	require.Equal(t, "/opt/tyrs-hand/codex/bin/codex", strings.TrimSpace(appServerExecutable))
	require.FileExists(t, filepath.Join(firstRepository, "one.txt"))
	require.Equal(t, "/home/dev", first.Home)
	require.NoError(t, dockerExec(manager, first, "sh", "-c",
		"printf home > $HOME/home-marker && printf forum-one > forum-marker"))
	require.NoError(t, dockerExec(manager, first, "sh", "-c", `set -eu
mkdir -p "$HOME/.local/share/tyrs-hand/codex/versions/0.145.0-user/bin"
cp /opt/tyrs-hand/codex/bin/codex "$HOME/.local/share/tyrs-hand/codex/versions/0.145.0-user/bin/codex"
ln -s "$HOME/.local/share/tyrs-hand/codex/versions/0.145.0-user" "$HOME/.local/share/tyrs-hand/codex/current"`))
	_, err = manager.docker(ctx, "exec", "--user", "0:0", first.Container, "sh", "-c",
		"printf system > /system-layer")
	require.NoError(t, err)

	conversationTwo := mustUUID(t, "10000000-0000-0000-0000-000000000002")
	second, err := manager.Ensure(ctx, environmentID, secondForumID, conversationTwo, "")
	require.NoError(t, err)
	loadedSecond, err := manager.Runtime(ctx, environmentID, secondForumID, conversationTwo)
	require.NoError(t, err)
	require.Equal(t, second.Container, loadedSecond.Container)
	require.Equal(t, second.Workspace, loadedSecond.Workspace)
	require.Equal(t, first.Container, second.Container)
	require.NotEqual(t, first.Workspace, second.Workspace)
	require.Equal(t, "two", dockerRead(t, manager, second, second.Workspace+"/two.txt"))
	require.Equal(t, "forum-one", dockerRead(t, manager, first, first.Workspace+"/forum-marker"))
	selection, err := manager.docker(ctx, "exec", "--user", "1001:1001", "--env",
		"HOME=/home/dev", first.Container, "tyrs-hand-dev", "codex", "status")
	require.NoError(t, err)
	require.Equal(t, "0.145.0-user", strings.TrimSpace(selection))
	require.Equal(t, "home", dockerRead(t, manager, second, second.Home+"/home-marker"))

	copySource := filepath.Join(root, "copy-source")
	require.NoError(t, os.MkdirAll(copySource, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(copySource, "copied.txt"), []byte("copied"), 0o600))
	require.NoError(t, manager.CopyToRuntime(ctx, first, copySource, first.Workspace+"/copied"))
	require.Equal(t, "copied", dockerRead(t, manager, first, first.Workspace+"/copied/copied.txt"))

	launcher := manager.Launcher(first)
	process, err := launcher.Launch(codex.ProcessSpec{Bin: "/bin/sh", Dir: first.Workspace,
		Args: []string{"-c", "printf launched"}, Env: []string{"TEST_LAUNCHER=1"}})
	require.NoError(t, err)
	require.NoError(t, process.Stdin().Close())
	launched, err := io.ReadAll(process.Stdout())
	require.NoError(t, err)
	_, _ = io.ReadAll(process.Stderr())
	require.NoError(t, process.Wait())
	require.Equal(t, "launched", string(launched))
	require.Error(t, process.Signal(syscall.SIGTERM))
	require.Error(t, process.Kill())

	require.NoError(t, manager.StopRemoteAppServer(ctx, first.Container))
	require.Eventually(t, func() bool {
		_, processErr := manager.docker(ctx, "exec", first.Container,
			"test", "!", "-d", "/proc/"+appServerPID)
		return processErr == nil
	}, 5*time.Second, 100*time.Millisecond)
	require.NoError(t, manager.EnsureRemoteDaemons(ctx, workerprotocol.EnvironmentManifest{
		EnvironmentID: environmentID, ContainerName: first.Container,
		RuntimeUser: first.User, RuntimeUID: first.UID, RuntimeGID: first.GID,
		RuntimeHome: first.Home,
	}, first))

	require.NoError(t, dockerExec(manager, first, "sh", "-c", "printf committed > committed.txt"))
	status, err := manager.Git(ctx, first, "status", "--porcelain=v1")
	require.NoError(t, err)
	require.Contains(t, status, "committed.txt")
	commitSHA, err := manager.Commit(ctx, first, "test: persist clone")
	require.NoError(t, err)
	require.NotEmpty(t, commitSHA)
	_, err = manager.Git(ctx, first, "init", "--bare", first.Home+"/push.git")
	require.NoError(t, err)
	_, err = manager.Git(ctx, first, "remote", "set-url", "origin", first.Home+"/push.git")
	require.NoError(t, err)
	branch, publishedSHA, err := manager.Publish(ctx, first, "")
	require.NoError(t, err)
	require.Equal(t, "tyrs-hand/discord/1", branch)
	require.Equal(t, commitSHA, publishedSHA)
	unchangedSHA, err := manager.Commit(ctx, first, "test: no changes")
	require.NoError(t, err)
	require.Equal(t, commitSHA, unchangedSHA)

	_, err = manager.docker(ctx, "stop", first.Container)
	require.NoError(t, err)
	first, err = manager.Ensure(ctx, environmentID, firstForumID, conversationOne, "")
	require.NoError(t, err)
	require.Equal(t, "system", dockerRead(t, manager, first, "/system-layer"))
	require.Equal(t, "home", dockerRead(t, manager, first, first.Home+"/home-marker"))
	sweeperCtx, cancelSweeper := context.WithCancel(ctx)
	go manager.RunSweeper(sweeperCtx)
	time.Sleep(50 * time.Millisecond)
	cancelSweeper()
	var runningStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM discord_development_environments WHERE id = $1`,
		environmentID).Scan(&runningStatus))
	require.Equal(t, "running", runningStatus)
	first, err = manager.Ensure(ctx, environmentID, firstForumID, conversationOne, "")
	require.NoError(t, err)
	testRemoteSSHAndDesktopProxy(t, manager, environmentID, first)

	rebaseImage := buildDevelopmentTestImage(t, manager, root, "dev", 1001, "rebase")
	_, err = db.ExecContext(ctx, `UPDATE discord_development_environments
		SET status = 'pending', image_ref = $2 WHERE id = $1`, environmentID, rebaseImage)
	require.NoError(t, err)
	first, err = manager.Ensure(ctx, environmentID, firstForumID, conversationOne, "")
	require.NoError(t, err)
	require.Equal(t, "home", dockerRead(t, manager, first, first.Home+"/home-marker"))
	require.Equal(t, "forum-one", dockerRead(t, manager, first, first.Workspace+"/forum-marker"))
	_, err = manager.docker(ctx, "exec", first.Container, "test", "!", "-e", "/system-layer")
	require.NoError(t, err, "重建必须重置系统可写层")
	require.Equal(t, "rebase", dockerRead(t, manager, first, "/image-version"))

	invalidImage := buildDevelopmentTestImage(t, manager, root, "other", 1002, "invalid-user")
	_, err = db.ExecContext(ctx, `UPDATE discord_development_environments
		SET status = 'pending', image_ref = $2 WHERE id = $1`, environmentID, invalidImage)
	require.NoError(t, err)
	_, err = manager.Ensure(ctx, environmentID, firstForumID, conversationOne, "")
	require.ErrorContains(t, err, "改变了 USER、UID/GID 或 Home")
	require.Equal(t, "home", dockerRead(t, manager, first, first.Home+"/home-marker"))
	require.Equal(t, "rebase", dockerRead(t, manager, first, "/image-version"))
	var unsupportedOperationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_development_operations
		(environment_id, forum_id, operation) VALUES ($1, $2, 'rebase') RETURNING id`,
		environmentID, secondForumID).Scan(&unsupportedOperationID))
	manager.processOperation(ctx)
	var unsupportedStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM discord_development_operations WHERE id = $1`,
		unsupportedOperationID).Scan(&unsupportedStatus))
	require.Equal(t, "failed", unsupportedStatus)

	var dataVolume, homeVolume, network string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT data_volume_name, home_volume_name, network_name
		FROM discord_development_environments WHERE id = $1`, environmentID).
		Scan(&dataVolume, &homeVolume, &network))
	_, err = db.ExecContext(ctx, `INSERT INTO discord_development_operations
		(environment_id, forum_id, operation) VALUES ($1, $2, 'delete_forum')`, environmentID, secondForumID)
	require.NoError(t, err)
	manager.processOperation(ctx)
	var secondForumCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_forums WHERE id = $1`,
		secondForumID).Scan(&secondForumCount))
	require.Zero(t, secondForumCount)
	var operationType string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type FROM integration_outbox
		WHERE operation_key = $1`, "development-forum-delete:"+secondForumID.String()).Scan(&operationType))
	require.Equal(t, "channel.delete", operationType)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_development_operations
		(environment_id, forum_id, operation) VALUES ($1, $2, 'delete_environment')`, environmentID, firstForumID)
	require.NoError(t, err)
	manager.processOperation(ctx)
	var environmentCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_development_environments WHERE id = $1`,
		environmentID).Scan(&environmentCount))
	require.Zero(t, environmentCount)
	for kind, name := range map[string]string{"volume": dataVolume, "home": homeVolume, "network": network} {
		inspectKind := kind
		if kind == "home" {
			inspectKind = "volume"
		}
		_, inspectErr := manager.docker(ctx, inspectKind, "inspect", name)
		require.Error(t, inspectErr)
	}
}

func developmentTestDockerfile(user string, uid int, version string) string {
	return fmt.Sprintf(`FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818
LABEL ai.tyrs-hand.development.contract="1"
RUN apt-get update && apt-get install --yes --no-install-recommends git=1:2.39.5-0+deb12u3 openssh-client=1:9.2p1-2+deb12u10 openssh-server=1:9.2p1-2+deb12u10 && rm -rf /var/lib/apt/lists/*
RUN useradd --uid %d --create-home --home-dir /home/%s %s && install -d -m 0755 /opt/tyrs-hand/codex/bin && printf '%s' > /image-version
COPY codex-real /opt/tyrs-hand/codex/bin/codex
COPY tyrs-hand-codex /usr/local/bin/codex
COPY tyrs-hand-dev /usr/local/bin/tyrs-hand-dev
RUN ln -s /usr/local/bin/codex /usr/local/bin/apply_patch
USER %s
`, uid, user, user, version, user)
}

func buildDevelopmentTestImage(t *testing.T, manager *Manager, target, user string,
	uid int, version string,
) string {
	t.Helper()
	arch, err := manager.docker(context.Background(), "version", "--format", "{{.Server.Arch}}")
	require.NoError(t, err)
	switch strings.TrimSpace(arch) {
	case "x86_64":
		arch = "amd64"
	case "aarch64":
		arch = "arm64"
	default:
		arch = strings.TrimSpace(arch)
	}
	repository := repositoryRoot(t)
	build := func(output, packagePath string) {
		path := filepath.Join(target, output)
		command := exec.Command("go", "build", "-o", path, packagePath)
		command.Dir = repository
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		data, buildErr := command.CombinedOutput()
		require.NoError(t, buildErr, string(data))
	}
	build("codex-real", "./internal/testutil/mockcodexapp")
	build("tyrs-hand-codex", "./cmd/tyrs-hand-codex")
	build("tyrs-hand-dev", "./cmd/tyrs-hand-dev")
	require.NoError(t, os.WriteFile(filepath.Join(target, "Dockerfile.development-test"),
		[]byte(developmentTestDockerfile(user, uid, version)), 0o600))
	image := "tyrs-hand-development-test:" + strings.ToLower(user) + "-" +
		strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = manager.docker(context.Background(), "build", "--file",
		filepath.Join(target, "Dockerfile.development-test"), "--tag", image, target)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = manager.docker(context.Background(), "image", "rm", image) })
	return image
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		require.NotEqual(t, directory, parent, "找不到测试仓库根目录")
		directory = parent
	}
}

func testRemoteSSHAndDesktopProxy(t *testing.T, manager *Manager, environmentID uuid.UUID,
	runtime Runtime,
) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(crand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(private)
	require.NoError(t, err)
	require.Equal(t, public, private.Public())
	authorizedKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())

	operation := RemoteOperation{EnvironmentID: environmentID, ContainerName: runtime.Container,
		RuntimeUser: runtime.User, RuntimeUID: runtime.UID, RuntimeGID: runtime.GID,
		RuntimeHome: runtime.Home, SSHPublicKey: authorizedKey, SSHPort: port,
		SSHConfigRevision: 1}
	require.NoError(t, manager.db.QueryRowContext(context.Background(), `SELECT image_ref,
		data_volume_name, home_volume_name, network_name FROM discord_development_environments
		WHERE id=$1`, environmentID).Scan(&operation.ImageRef, &operation.DataVolume,
		&operation.HomeVolume, &operation.Network))
	require.NoError(t, manager.reconfigureRemote(context.Background(), operation))
	permissions, err := manager.docker(context.Background(), "exec", runtime.Container,
		"stat", "-c", "%a:%n", "/run/tyrs-hand", "/run/tyrs-hand/app-server.sock")
	require.NoError(t, err)
	require.Contains(t, permissions, "777:/run/tyrs-hand\n")
	require.Contains(t, permissions, "666:/run/tyrs-hand/app-server.sock")

	hostKeyBefore := dockerReadRoot(t, manager, runtime,
		"/var/lib/tyrs-hand/system/ssh/ssh_host_ed25519_key.pub")
	appServerArguments := []string{"exec", "--detach", "--user",
		fmt.Sprintf("%d:%d", runtime.UID, runtime.GID), runtime.Container,
		"/opt/tyrs-hand/codex/bin/codex"}
	appServerArguments = append(appServerArguments,
		codex.ManagedAppServerArguments("unix:///run/tyrs-hand/relay.sock")...)
	_, err = manager.docker(context.Background(), appServerArguments...)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, socketErr := manager.docker(context.Background(), "exec", runtime.Container,
			"test", "-S", "/run/tyrs-hand/relay.sock")
		return socketErr == nil
	}, 5*time.Second, 100*time.Millisecond)

	address := fmt.Sprintf("127.0.0.1:%d", port)
	client := dialSSHEventually(t, address, &ssh.ClientConfig{User: runtime.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout: 5 * time.Second})
	session, err := client.NewSession()
	require.NoError(t, err)
	version, err := session.CombinedOutput("codex --version")
	require.NoError(t, err, string(version))
	require.Equal(t, "codex-cli 0.145.0", strings.TrimSpace(string(version)))
	_ = session.Close()

	sftp, err := client.NewSession()
	require.NoError(t, err)
	require.NoError(t, sftp.RequestSubsystem("sftp"))
	_ = sftp.Close()

	proxy, err := client.NewSession()
	require.NoError(t, err)
	stdin, err := proxy.StdinPipe()
	require.NoError(t, err)
	stdout, err := proxy.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, proxy.Start("codex app-server proxy"))
	_, err = io.WriteString(stdin, "GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\n"+
		"Connection: Upgrade\r\nSec-WebSocket-Version: 13\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	require.NoError(t, err)
	reader := bufio.NewReader(stdout)
	status, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Contains(t, status, "101 Switching Protocols")
	for {
		line, readErr := reader.ReadString('\n')
		require.NoError(t, readErr)
		if line == "\r\n" {
			break
		}
	}
	_ = proxy.Close()

	t.Run("真实 Codex 与 Mock LLM 接收 SSH 绑定身份", func(t *testing.T) {
		testSSHProxyParticipantIdentity(t, manager, runtime, client)
	})

	_, err = client.Dial("tcp", "127.0.0.1:1")
	require.Error(t, err, "sshd 必须拒绝端口转发")
	require.NoError(t, client.Close())
	assertSSHDialRejected(t, address, &ssh.ClientConfig{User: runtime.User,
		Auth: []ssh.AuthMethod{ssh.Password("wrong")}, HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout: 3 * time.Second})
	_, wrongPrivate, err := ed25519.GenerateKey(crand.Reader)
	require.NoError(t, err)
	wrongSigner, err := ssh.NewSignerFromKey(wrongPrivate)
	require.NoError(t, err)
	assertSSHDialRejected(t, address, &ssh.ClientConfig{User: runtime.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(wrongSigner)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout: 3 * time.Second})
	assertSSHDialRejected(t, address, &ssh.ClientConfig{User: "root",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout: 3 * time.Second})

	operation.SSHConfigRevision = 2
	require.NoError(t, manager.reconfigureRemote(context.Background(), operation))
	hostKeyAfter := dockerReadRoot(t, manager, runtime,
		"/var/lib/tyrs-hand/system/ssh/ssh_host_ed25519_key.pub")
	require.Equal(t, hostKeyBefore, hostKeyAfter, "容器重建后 SSH Host Key 必须稳定")
}

func testSSHProxyParticipantIdentity(t *testing.T, manager *Manager, runtime Runtime,
	sshClient *ssh.Client,
) {
	t.Helper()
	if goruntime.GOOS != "linux" {
		t.Skip("Docker Desktop 不支持容器与宿主之间的 Unix Socket 双向连接")
	}
	codexBin := exactIntegrationCodexBinary(t)
	require.NoError(t, manager.StopRemoteAppServer(context.Background(), runtime.Container))
	require.Eventually(t, func() bool {
		_, err := os.Stat(runtime.AppServerSocket)
		return os.IsNotExist(err)
	}, 5*time.Second, 100*time.Millisecond)

	requests := make(chan string, 2)
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		requests <- string(body)
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(response, mockResponsesStream("ssh-identity-response"))
	}))
	t.Cleanup(responses.Close)

	root := t.TempDir()
	codexHome, workspace := filepath.Join(root, "codex-home"), filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(codexHome, 0o700))
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	configBody := fmt.Sprintf(`model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "SSH identity mock"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`, responses.URL+"/v1")
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "config.toml"),
		[]byte(configBody), 0o600))

	appServer := exec.Command(codexBin, "app-server", "--listen",
		"unix://"+runtime.AppServerSocket)
	appServer.Dir = workspace
	appServer.Env = append(os.Environ(), "CODEX_HOME="+codexHome, "HOME="+root, "RUST_LOG=warn")
	require.NoError(t, appServer.Start())
	defer func() {
		_ = appServer.Process.Kill()
		_ = appServer.Wait()
		_ = os.Remove(runtime.AppServerSocket)
	}()
	require.Eventually(t, func() bool {
		info, err := os.Stat(runtime.AppServerSocket)
		return err == nil && info.Mode()&os.ModeSocket != 0
	}, 10*time.Second, 100*time.Millisecond)

	participant := participantidentity.Participant{
		ID: participantidentity.ID("100000000000000001", "1001"), DisplayName: "Owner",
	}
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: runtime.RelaySocket, UpstreamSocketPath: runtime.AppServerSocket,
		Controller: sshIdentityController{participant: participant},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, relay.Close()) }()

	rpc := openSSHCodexRPC(t, sshClient)
	defer rpc.close()
	var initialize json.RawMessage
	require.NoError(t, rpc.call("initialize", map[string]any{
		"clientInfo": map[string]string{
			"name": "codex-desktop", "title": "Codex Desktop", "version": "test",
		},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &initialize))
	require.NoError(t, rpc.ws.WriteJSON(map[string]any{
		"method": "initialized", "params": map[string]any{},
	}))
	var threadResult struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	require.NoError(t, rpc.call("thread/start", map[string]any{
		"cwd": workspace, "model": "mock-model", "approvalPolicy": "never",
		"sandbox": "read-only",
	}, &threadResult))
	require.NotEmpty(t, threadResult.Thread.ID)
	var turnResult struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, rpc.call("turn/start", map[string]any{
		"threadId": threadResult.Thread.ID,
		"input": []map[string]any{{"type": "text", "text": "SSH Desktop identity",
			"textElements": []any{}}},
		"additionalContext": map[string]any{
			participantidentity.IdentityContextKey: map[string]string{
				"kind": "application", "value": `{"participant_id":"forged"}`,
			},
		},
	}, &turnResult))
	require.NoError(t, rpc.waitTurnCompleted(threadResult.Thread.ID, turnResult.Turn.ID))

	select {
	case body := <-requests:
		require.Contains(t, body, participant.ID.String())
		require.Contains(t, body, participant.DisplayName)
		require.NotContains(t, body, "forged")
	case <-time.After(10 * time.Second):
		t.Fatal("Mock LLM 没有收到 SSH Desktop Turn")
	}
}

type sshIdentityController struct {
	participant participantidentity.Participant
}

func (c sshIdentityController) PrepareCall(_ context.Context,
	call codexrelay.Call,
) (codexrelay.CallPlan, error) {
	params := append(json.RawMessage(nil), call.Params...)
	if call.Method == "thread/start" {
		params = participantidentity.AppendDeveloperInstructions(params)
	}
	if call.Method == "turn/start" || call.Method == "turn/steer" {
		params = participantidentity.InjectTurnContext(params, c.participant)
	}
	return codexrelay.CallPlan{Params: params, Forward: true}, nil
}

func (sshIdentityController) CompleteCall(_ context.Context, _ codexrelay.Call,
	_ codexrelay.CallPlan, result json.RawMessage, cause error,
) (json.RawMessage, error) {
	return result, cause
}

func (sshIdentityController) ResolveInteractive(_ context.Context, _ codex.ServerRequest,
	answer json.RawMessage, _ codexrelay.Role,
) (bool, json.RawMessage, error) {
	return true, answer, nil
}

type sshCodexRPC struct {
	ws      *websocket.Conn
	session *ssh.Session
	nextID  int64
	events  []sshRPCMessage
}

type sshRPCMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func openSSHCodexRPC(t *testing.T, client *ssh.Client) *sshCodexRPC {
	t.Helper()
	session, err := client.NewSession()
	require.NoError(t, err)
	stdin, err := session.StdinPipe()
	require.NoError(t, err)
	stdout, err := session.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, session.Start("codex app-server proxy"))
	connection := &sshSessionConn{reader: stdout, writer: stdin, session: session}
	ws, response, err := websocket.NewClient(connection, &url.URL{
		Scheme: "ws", Host: "localhost", Path: "/",
	}, nil, 4096, 4096)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	return &sshCodexRPC{ws: ws, session: session}
}

func (c *sshCodexRPC) call(method string, params any, result any) error {
	c.nextID++
	id := c.nextID
	if err := c.ws.WriteJSON(map[string]any{
		"id": id, "method": method, "params": params,
	}); err != nil {
		return err
	}
	for {
		var message sshRPCMessage
		if err := c.ws.ReadJSON(&message); err != nil {
			return err
		}
		if len(message.ID) == 0 {
			c.events = append(c.events, message)
			continue
		}
		if string(message.ID) != strconv.FormatInt(id, 10) {
			continue
		}
		if message.Error != nil {
			return fmt.Errorf("%s: app-server RPC %d: %s", method,
				message.Error.Code, message.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(message.Result, result)
		}
		return nil
	}
}

func (c *sshCodexRPC) waitTurnCompleted(threadID, turnID string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		for index, message := range c.events {
			if completedSSHEvent(message, threadID, turnID) {
				c.events = append(c.events[:index], c.events[index+1:]...)
				return nil
			}
		}
		if err := c.ws.SetReadDeadline(deadline); err != nil {
			return err
		}
		var message sshRPCMessage
		if err := c.ws.ReadJSON(&message); err != nil {
			return err
		}
		if completedSSHEvent(message, threadID, turnID) {
			return nil
		}
		c.events = append(c.events, message)
	}
}

func completedSSHEvent(message sshRPCMessage, threadID, turnID string) bool {
	if message.Method != "turn/completed" {
		return false
	}
	var value struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	return json.Unmarshal(message.Params, &value) == nil &&
		value.ThreadID == threadID && value.Turn.ID == turnID
}

func (c *sshCodexRPC) close() {
	_ = c.ws.Close()
	_ = c.session.Close()
}

type sshSessionConn struct {
	reader  io.Reader
	writer  io.WriteCloser
	session *ssh.Session
}

func (c *sshSessionConn) Read(buffer []byte) (int, error)  { return c.reader.Read(buffer) }
func (c *sshSessionConn) Write(buffer []byte) (int, error) { return c.writer.Write(buffer) }
func (c *sshSessionConn) Close() error {
	_ = c.writer.Close()
	return c.session.Close()
}
func (c *sshSessionConn) LocalAddr() net.Addr              { return sshProxyAddr("local") }
func (c *sshSessionConn) RemoteAddr() net.Addr             { return sshProxyAddr("remote") }
func (c *sshSessionConn) SetDeadline(time.Time) error      { return nil }
func (c *sshSessionConn) SetReadDeadline(time.Time) error  { return nil }
func (c *sshSessionConn) SetWriteDeadline(time.Time) error { return nil }

type sshProxyAddr string

func (a sshProxyAddr) Network() string { return "ssh-stdio" }
func (a sshProxyAddr) String() string  { return string(a) }

func exactIntegrationCodexBinary(t *testing.T) string {
	t.Helper()
	name := os.Getenv("TYRS_HAND_TEST_CODEX_BIN")
	if name == "" {
		name = "codex"
	}
	binary, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("CI") == "true" {
			require.NoError(t, err, "CI 缺少固定 Codex 0.145.0")
		}
		t.Skip("本机缺少固定 Codex 0.145.0")
	}
	output, err := exec.Command(binary, "--version").CombinedOutput()
	require.NoError(t, err)
	if strings.TrimSpace(string(output)) != "codex-cli 0.145.0" {
		if os.Getenv("CI") == "true" {
			require.Equal(t, "codex-cli 0.145.0", strings.TrimSpace(string(output)))
		}
		t.Skip("本机 Codex 不是固定版本 0.145.0")
	}
	return binary
}

func mockResponsesStream(id string) string {
	events := []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id}},
		{"type": "response.output_item.done", "item": map[string]any{
			"type": "message", "role": "assistant", "id": "ssh-identity-message",
			"content": []map[string]any{{"type": "output_text", "text": "done"}},
		}},
		{"type": "response.completed", "response": map[string]any{
			"id": id, "usage": map[string]any{
				"input_tokens": 0, "input_tokens_details": nil, "output_tokens": 0,
				"output_tokens_details": nil, "total_tokens": 0,
			},
		}},
	}
	var result strings.Builder
	for _, event := range events {
		data, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(&result, "event: %s\ndata: %s\n\n", event["type"], data)
	}
	return result.String()
}

func dialSSHEventually(t *testing.T, address string, configuration *ssh.ClientConfig) *ssh.Client {
	t.Helper()
	var client *ssh.Client
	var err error
	require.Eventually(t, func() bool {
		client, err = ssh.Dial("tcp", address, configuration)
		return err == nil
	}, 15*time.Second, 200*time.Millisecond, "SSH 未就绪: %v", err)
	return client
}

func assertSSHDialRejected(t *testing.T, address string, configuration *ssh.ClientConfig) {
	t.Helper()
	client, err := ssh.Dial("tcp", address, configuration)
	if client != nil {
		_ = client.Close()
	}
	require.Error(t, err)
}

func createGitRepository(t *testing.T, directory, filename, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(directory, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(directory, filename), []byte(content), 0o600))
	runGit(t, directory, "init", "--initial-branch=main")
	runGit(t, directory, "add", ".")
	runGit(t, directory, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"commit", "-m", "initial")
	return directory
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func dockerExec(manager *Manager, runtime Runtime, arguments ...string) error {
	base := []string{"exec", "--user", fmt.Sprintf("%d:%d", runtime.UID, runtime.GID),
		"--env", "HOME=" + runtime.Home, "--workdir", runtime.Workspace, runtime.Container}
	_, err := manager.docker(context.Background(), append(base, arguments...)...)
	return err
}

func dockerRead(t *testing.T, manager *Manager, runtime Runtime, path string) string {
	t.Helper()
	value, err := manager.docker(context.Background(), "exec", runtime.Container, "cat", path)
	require.NoError(t, err)
	return strings.TrimSpace(value)
}

func dockerReadRoot(t *testing.T, manager *Manager, runtime Runtime, path string) string {
	t.Helper()
	value, err := manager.docker(context.Background(), "exec", "--user", "0:0",
		runtime.Container, "cat", path)
	require.NoError(t, err)
	return strings.TrimSpace(value)
}

func seedDevelopmentEnvironment(t *testing.T, db *sql.DB, firstClone, secondClone,
	imageRef string,
) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, enabled)
		VALUES ('100000000000000001', true)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_members
		(guild_id, discord_user_id, username, display_name)
		VALUES ('100000000000000001', '1001', 'owner', 'Owner')`)
	require.NoError(t, err)
	var installationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO scm_installations
		(provider, external_id, account_login, account_type)
		VALUES ('github', 7001, 'owner', 'Organization') RETURNING id`).Scan(&installationID))
	insertRepository := func(externalID int64, name, clone string) uuid.UUID {
		var id uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO repositories
			(installation_id, provider, external_id, owner, name, default_branch, clone_url)
			VALUES ($1, 'github', $2, 'owner', $3, 'main', $4) RETURNING id`,
			installationID, externalID, name, clone).Scan(&id))
		return id
	}
	firstRepositoryID := insertRepository(7002, "first", firstClone)
	secondRepositoryID := insertRepository(7003, "second", secondClone)
	environmentID := uuid.New()
	compact := strings.ReplaceAll(environmentID.String(), "-", "")
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_development_environments
		(id, guild_id, owner_discord_user_id, image_ref, container_name,
		 data_volume_name, home_volume_name, network_name)
		VALUES ($1, '100000000000000001', '1001', $2, $3, $4, $5, $6) RETURNING id`,
		environmentID, imageRef, "tyrs-test-dev-"+compact, "tyrs-test-data-"+compact,
		"tyrs-test-home-"+compact, "tyrs-test-net-"+compact).Scan(&environmentID))
	insertForum := func(suffix string, repositoryID uuid.UUID) uuid.UUID {
		forumID := uuid.New()
		var resourceID uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_resources
			(guild_id, resource_key, discord_id, kind, name, managed_marker)
			VALUES ('100000000000000001', $1, $2, 'forum', $3, $1) RETURNING id`,
			"forum.development."+suffix, "800"+suffix, "dev-"+suffix).Scan(&resourceID))
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_forums
			(id, guild_id, resource_id, forum_type, owner_discord_user_id,
			 repository_id, development_environment_id)
			VALUES ($1, '100000000000000001', $2, 'development', '1001', $3, $4) RETURNING id`,
			forumID, resourceID, repositoryID, environmentID).Scan(&forumID))
		_, err := db.ExecContext(ctx, `INSERT INTO discord_forum_workspaces
			(forum_id, environment_id, relative_path, branch)
			VALUES ($1, $2, $3, $4)`, forumID, environmentID, "workspaces/"+forumID.String(),
			"tyrs-hand/discord/"+suffix)
		require.NoError(t, err)
		return forumID
	}
	return environmentID, insertForum("1", firstRepositoryID), insertForum("2", secondRepositoryID)
}

func cleanupDevelopmentResources(manager *Manager, environmentID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var container, dataVolume, homeVolume, network, imageRef string
	err := manager.db.QueryRowContext(ctx, `SELECT container_name, data_volume_name, home_volume_name,
		network_name, COALESCE(image_ref, '') FROM discord_development_environments WHERE id = $1`,
		environmentID).Scan(&container, &dataVolume, &homeVolume, &network, &imageRef)
	if err != nil {
		return
	}
	_, _ = manager.docker(ctx, "rm", "--force", container)
	_, _ = manager.docker(ctx, "volume", "rm", dataVolume)
	_, _ = manager.docker(ctx, "volume", "rm", homeVolume)
	_, _ = manager.docker(ctx, "network", "rm", network)
	if imageRef != "" {
		_, _ = manager.docker(ctx, "image", "rm", "--force", imageRef)
	}
}

func developmentDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:18.3-bookworm@sha256:80630f83606d8db77d30b3851b16a9f78be2d0d4dda6f7b82a1fdca5ebe3acba",
			Env:          map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "test"},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor:   wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		}, Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:test@%s:%s/test?sslmode=disable", host, port.Port()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.Eventually(t, func() bool { return db.PingContext(ctx) == nil }, 15*time.Second, 100*time.Millisecond)
	return db
}

func mustUUID(t *testing.T, value string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(value)
	require.NoError(t, err)
	return id
}
