//go:build integration

package devcontainer

import (
	"bufio"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
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
	buildRepository := createGitRepository(t, filepath.Join(root, "build-repo"), "one.txt", "one")
	secondRepository := createGitRepository(t, filepath.Join(root, "second-repo"), "two.txt", "two")
	environmentID, firstForumID, secondForumID := seedDevelopmentEnvironment(t, db,
		buildRepository, secondRepository)
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
	manager.codexBin, manager.codexProxyBin = buildLinuxCodexTestBinaries(t, manager, root)
	manager.replyHook = filepath.Join(root, "missing-reply-hook")
	t.Cleanup(func() { cleanupDevelopmentResources(manager, environmentID) })

	conversationOne := mustUUID(t, "10000000-0000-0000-0000-000000000001")
	first, err := manager.Ensure(ctx, environmentID, firstForumID, conversationOne, "")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(buildRepository, "one.txt"))
	require.Equal(t, "/home/dev", first.Home)
	require.NoError(t, dockerExec(manager, first, "sh", "-c",
		"printf home > $HOME/home-marker && printf forum-one > forum-marker"))
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

	commitDockerfile(t, buildRepository, dockerfile("dev", 1001, "rebuild"))
	_, err = db.ExecContext(ctx, `UPDATE discord_development_environments SET status = 'pending' WHERE id = $1`, environmentID)
	require.NoError(t, err)
	first, err = manager.Ensure(ctx, environmentID, firstForumID, conversationOne, "")
	require.NoError(t, err)
	require.Equal(t, "home", dockerRead(t, manager, first, first.Home+"/home-marker"))
	require.Equal(t, "forum-one", dockerRead(t, manager, first, first.Workspace+"/forum-marker"))
	_, err = manager.docker(ctx, "exec", first.Container, "test", "!", "-e", "/system-layer")
	require.NoError(t, err, "重建必须重置系统可写层")
	require.Equal(t, "rebuild", dockerRead(t, manager, first, "/image-version"))

	commitDockerfile(t, buildRepository, dockerfile("other", 1002, "invalid-user"))
	_, err = db.ExecContext(ctx, `UPDATE discord_development_environments SET status = 'pending' WHERE id = $1`, environmentID)
	require.NoError(t, err)
	_, err = manager.Ensure(ctx, environmentID, firstForumID, conversationOne, "")
	require.ErrorContains(t, err, "改变了 USER、UID/GID 或 Home")
	require.Equal(t, "home", dockerRead(t, manager, first, first.Home+"/home-marker"))
	require.Equal(t, "rebuild", dockerRead(t, manager, first, "/image-version"))
	var unsupportedOperationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_development_operations
		(environment_id, forum_id, operation) VALUES ($1, $2, 'rebuild') RETURNING id`,
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

func dockerfile(user string, uid int, version string) string {
	return fmt.Sprintf(`FROM debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818
RUN apt-get update && apt-get install --yes --no-install-recommends git=1:2.39.5-0+deb12u3 openssh-client=1:9.2p1-2+deb12u10 openssh-server=1:9.2p1-2+deb12u10 && rm -rf /var/lib/apt/lists/*
RUN useradd --uid %d --create-home --home-dir /home/%s %s && printf '%s' > /image-version
USER %s
`, uid, user, user, version, user)
}

func buildLinuxCodexTestBinaries(t *testing.T, manager *Manager, target string) (string, string) {
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
	build := func(output, packagePath string) string {
		path := filepath.Join(target, output)
		command := exec.Command("go", "build", "-o", path, packagePath)
		command.Dir = repository
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		data, buildErr := command.CombinedOutput()
		require.NoError(t, buildErr, string(data))
		return path
	}
	return build("codex-real", "./internal/testutil/mockcodexapp"),
		build("tyrs-hand-codex", "./cmd/tyrs-hand-codex")
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
	_, err = manager.docker(context.Background(), "exec", "--detach", "--user",
		fmt.Sprintf("%d:%d", runtime.UID, runtime.GID), runtime.Container,
		"/opt/tyrs-hand/libexec/codex-real", "app-server", "--listen",
		"unix:///run/tyrs-hand/relay.sock")
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
	require.Equal(t, "codex-cli 0.142.5", strings.TrimSpace(string(version)))
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
	require.NoError(t, os.MkdirAll(filepath.Join(directory, ".devcontainer"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(directory, ".devcontainer", "Dockerfile"),
		[]byte(dockerfile("dev", 1001, "initial")), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, filename), []byte(content), 0o600))
	runGit(t, directory, "init", "--initial-branch=main")
	runGit(t, directory, "add", ".")
	runGit(t, directory, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"commit", "-m", "initial")
	return directory
}

func commitDockerfile(t *testing.T, repository, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repository, ".devcontainer", "Dockerfile"), []byte(content), 0o600))
	runGit(t, repository, "add", ".devcontainer/Dockerfile")
	runGit(t, repository, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"commit", "-m", "change image")
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

func seedDevelopmentEnvironment(t *testing.T, db *sql.DB, firstClone, secondClone string) (uuid.UUID, uuid.UUID, uuid.UUID) {
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
		(id, guild_id, owner_discord_user_id, build_repository_id, container_name,
		 data_volume_name, home_volume_name, network_name)
		VALUES ($1, '100000000000000001', '1001', $2, $3, $4, $5, $6) RETURNING id`,
		environmentID, firstRepositoryID, "tyrs-test-dev-"+compact, "tyrs-test-data-"+compact,
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
