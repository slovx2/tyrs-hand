package devcontainer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"go.uber.org/zap"
)

const defaultDockerBinary = "/usr/local/libexec/tyrs-hand/docker"

type Manager struct {
	db                        *sql.DB
	dataRoot                  string
	dockerBin                 string
	dockerHost                string
	codexBin                  string
	codexProxyBin             string
	replyHook                 string
	runner                    commandRunner
	logger                    *zap.Logger
	enabled                   bool
	developmentRuntimeDir     string
	developmentRuntimeHostDir string
	sshEnabled                bool
	sshAgentDir               string
	sshAgentHostDir           string
	browserEnabled            bool
	browserFilesRoot          string
	browserFilesHostRoot      string
}

func NewManager(cfg config.Config, db *sql.DB, logger *zap.Logger) (*Manager, error) {
	binary := os.Getenv("TYRS_HAND_DOCKER_REAL_BIN")
	if binary == "" {
		binary = defaultDockerBinary
	}
	dockerHost := os.Getenv("TYRS_HAND_DOCKER_HOST")
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	runtimeDir, runtimeHostDir := cfg.DevelopmentRuntimeDir, cfg.DevelopmentRuntimeHostDir
	if !filepath.IsAbs(runtimeDir) {
		runtimeDir, _ = filepath.Abs(runtimeDir)
	}
	if !filepath.IsAbs(runtimeHostDir) {
		runtimeHostDir, _ = filepath.Abs(runtimeHostDir)
	}
	manager := &Manager{
		db: db, dataRoot: cfg.WorkerDataRoot, dockerBin: binary, dockerHost: dockerHost,
		codexBin: "/usr/local/bin/apply_patch", codexProxyBin: "/usr/local/bin/tyrs-hand-codex",
		replyHook: "/usr/local/bin/tyrs-hand-reply-hook",
		runner:    execRunner{}, logger: logger,
		enabled:               cfg.EnableDevelopmentContainers && (cfg.WorkerRole == "discord" || cfg.WorkerRole == "all"),
		developmentRuntimeDir: runtimeDir, developmentRuntimeHostDir: runtimeHostDir,
		sshEnabled: cfg.EnableSSH, sshAgentDir: cfg.SSHAgentDir,
		sshAgentHostDir: cfg.SSHAgentHostDir, browserEnabled: cfg.BrowserMCPURL != "",
		browserFilesRoot: cfg.BrowserFilesRoot, browserFilesHostRoot: cfg.BrowserFilesHostRoot,
	}
	if !manager.enabled {
		return manager, nil
	}
	if _, err := manager.docker(context.Background(), "version", "--format", "{{.Server.Version}}"); err != nil {
		return nil, fmt.Errorf("连接开发容器 Docker Daemon: %w", err)
	}
	return manager, nil
}

func (m *Manager) Enabled() bool { return m != nil && m.enabled }

func (m *Manager) Ensure(ctx context.Context, environmentID, forumID, conversationID uuid.UUID,
	credential string,
) (Runtime, error) {
	if !m.Enabled() {
		return Runtime{}, errors.New("discord 开发容器未启用")
	}
	connection, err := m.db.Conn(ctx)
	if err != nil {
		return Runtime{}, err
	}
	defer func() { _ = connection.Close() }()
	lockKey := "discord-development-environment:" + environmentID.String()
	if _, err := connection.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext($1))", lockKey); err != nil {
		return Runtime{}, err
	}
	defer func() {
		_, _ = connection.ExecContext(context.Background(), "SELECT pg_advisory_unlock(hashtext($1))", lockKey)
	}()

	item, err := m.loadWorkspace(ctx, environmentID, forumID)
	if err != nil {
		return Runtime{}, err
	}
	if item.Environment.Status == "pending" || item.Environment.Status == "error" || item.Environment.ContainerID == "" {
		if err := m.provision(ctx, &item, credential); err != nil {
			m.failEnvironment(environmentID, err)
			return Runtime{}, err
		}
	}
	if item.Status != "ready" {
		if err := m.cloneWorkspace(ctx, &item, credential); err != nil {
			m.failWorkspace(forumID, err)
			return Runtime{}, err
		}
	}
	if _, err := m.docker(ctx, "start", item.Environment.ContainerName); err != nil {
		return Runtime{}, err
	}
	codexHome := filepath.ToSlash(filepath.Join(containerRoot, "codex"))
	if _, err := m.docker(ctx, "exec", "--user", "0:0", item.Environment.ContainerName,
		"mkdir", "-p", codexHome); err != nil {
		return Runtime{}, err
	}
	owner := fmt.Sprintf("%d:%d", item.Environment.RuntimeUID, item.Environment.RuntimeGID)
	if _, err := m.docker(ctx, "exec", "--user", "0:0", item.Environment.ContainerName,
		"chown", owner, codexHome); err != nil {
		return Runtime{}, err
	}
	_, _ = m.db.ExecContext(ctx, `UPDATE discord_development_environments
		SET status = 'running', last_used_at = now(), error = NULL, updated_at = now()
		WHERE id = $1`, environmentID)
	return Runtime{
		EnvironmentID: environmentID, ForumID: forumID, Container: item.Environment.ContainerName,
		Workspace: filepath.ToSlash(filepath.Join(containerRoot, item.Relative)), CodexHome: codexHome,
		User: item.Environment.RuntimeUser, UID: item.Environment.RuntimeUID,
		GID: item.Environment.RuntimeGID, Home: item.Environment.RuntimeHome,
		AppServerSocket: filepath.Join(m.developmentRuntimeDir, environmentID.String(), "app-server.sock"),
		RelaySocket:     filepath.Join(m.developmentRuntimeDir, environmentID.String(), "relay.sock"),
	}, nil
}

func (m *Manager) Runtime(ctx context.Context, environmentID, forumID, conversationID uuid.UUID) (Runtime, error) {
	item, err := m.loadWorkspace(ctx, environmentID, forumID)
	if err != nil {
		return Runtime{}, err
	}
	if item.Environment.ContainerID == "" || item.Status != "ready" {
		return Runtime{}, errors.New("discord 开发环境尚未就绪")
	}
	return Runtime{
		EnvironmentID: environmentID, ForumID: forumID, Container: item.Environment.ContainerName,
		Workspace: filepath.ToSlash(filepath.Join(containerRoot, item.Relative)),
		CodexHome: filepath.ToSlash(filepath.Join(containerRoot, "codex")),
		User:      item.Environment.RuntimeUser, UID: item.Environment.RuntimeUID,
		GID: item.Environment.RuntimeGID, Home: item.Environment.RuntimeHome,
		AppServerSocket: filepath.Join(m.developmentRuntimeDir, environmentID.String(), "app-server.sock"),
		RelaySocket:     filepath.Join(m.developmentRuntimeDir, environmentID.String(), "relay.sock"),
	}, nil
}

func (m *Manager) loadWorkspace(ctx context.Context, environmentID, forumID uuid.UUID) (workspace, error) {
	var item workspace
	var imageRef, imageID, containerID, runtimeUser, runtimeHome, buildSHA sql.NullString
	err := m.db.QueryRowContext(ctx, `SELECT fw.forum_id, fw.relative_path, fw.status, fw.branch,
		r.owner || '/' || r.name, r.clone_url, r.default_branch,
		e.id, e.build_repository_id, br.owner || '/' || br.name, br.clone_url, br.default_branch,
		e.status, e.image_ref, e.image_id, e.container_name, e.container_id,
		e.data_volume_name, e.home_volume_name, e.network_name, e.runtime_user,
		COALESCE(e.runtime_uid, 0), COALESCE(e.runtime_gid, 0), e.runtime_home, e.build_source_sha
		FROM discord_forum_workspaces fw JOIN discord_development_environments e ON e.id = fw.environment_id
		JOIN discord_forums f ON f.id = fw.forum_id
		JOIN repositories r ON r.id = f.repository_id
		JOIN repositories br ON br.id = e.build_repository_id
		WHERE fw.forum_id = $1 AND e.id = $2`, forumID, environmentID).Scan(
		&item.ForumID, &item.Relative, &item.Status, &item.Branch,
		&item.Repository, &item.CloneURL, &item.DefaultRef,
		&item.Environment.ID, &item.Environment.BuildRepositoryID, &item.Environment.BuildRepository,
		&item.Environment.BuildCloneURL, &item.Environment.BuildDefaultRef, &item.Environment.Status,
		&imageRef, &imageID, &item.Environment.ContainerName, &containerID,
		&item.Environment.DataVolume, &item.Environment.HomeVolume, &item.Environment.Network,
		&runtimeUser, &item.Environment.RuntimeUID, &item.Environment.RuntimeGID, &runtimeHome, &buildSHA)
	item.Environment.ImageRef, item.Environment.ImageID = imageRef.String, imageID.String
	item.Environment.ContainerID, item.Environment.RuntimeUser = containerID.String, runtimeUser.String
	item.Environment.RuntimeHome, item.Environment.BuildSourceSHA = runtimeHome.String, buildSHA.String
	return item, err
}

func (m *Manager) docker(ctx context.Context, arguments ...string) (string, error) {
	environment := []string(nil)
	if m.dockerHost != "inherit" {
		environment = []string{"DOCKER_HOST=" + m.dockerHost}
	}
	return m.runner.Run(ctx, environment, "", append([]string{m.dockerBin}, arguments...)...)
}

func (m *Manager) failEnvironment(id uuid.UUID, cause error) {
	if m.db == nil {
		return
	}
	_, _ = m.db.ExecContext(context.Background(), `UPDATE discord_development_environments
		SET status = 'error', error = $2, updated_at = now() WHERE id = $1`, id, cause.Error())
}

func (m *Manager) failWorkspace(id uuid.UUID, cause error) {
	if m.db == nil {
		return
	}
	_, _ = m.db.ExecContext(context.Background(), `UPDATE discord_forum_workspaces
		SET status = 'error', error = $2, updated_at = now() WHERE forum_id = $1`, id, cause.Error())
}

func parseIdentity(value string) (int64, int64, string, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, 0, "", fmt.Errorf("镜像用户信息无效")
	}
	uid, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, "", err
	}
	gid, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, "", err
	}
	return uid, gid, parts[2], nil
}
