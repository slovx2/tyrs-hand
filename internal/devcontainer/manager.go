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
	"syscall"

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
	browserServicesRoot       string
	browserServicesHostRoot   string
	developmentImage          string
	hostDocker                bool
	dockerSocketGID           uint32
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
		runner: execRunner{}, logger: logger,
		enabled:               cfg.EnableDevelopmentContainers && (cfg.WorkerRole == "discord" || cfg.WorkerRole == "all"),
		developmentRuntimeDir: runtimeDir, developmentRuntimeHostDir: runtimeHostDir,
		sshEnabled: cfg.EnableSSH, sshAgentDir: cfg.SSHAgentDir,
		sshAgentHostDir: cfg.SSHAgentHostDir, browserEnabled: cfg.BrowserMCPURL != "",
		browserFilesRoot: cfg.BrowserFilesRoot, browserFilesHostRoot: cfg.BrowserFilesHostRoot,
		browserServicesRoot:     cfg.BrowserServicesRoot,
		browserServicesHostRoot: cfg.BrowserServicesHostRoot,
		developmentImage:        cfg.DevelopmentImage,
		hostDocker:              cfg.DevelopmentHostDocker,
	}
	if !manager.enabled {
		return manager, nil
	}
	if _, err := manager.docker(context.Background(), "version", "--format", "{{.Server.Version}}"); err != nil {
		return nil, fmt.Errorf("连接开发容器 Docker Daemon: %w", err)
	}
	if manager.hostDocker {
		info, err := os.Stat("/var/run/docker.sock")
		if err != nil {
			return nil, fmt.Errorf("检查宿主 Docker Socket: %w", err)
		}
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("宿主 Docker Socket 不是 Unix Socket")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, errors.New("读取宿主 Docker Socket GID 失败")
		}
		manager.dockerSocketGID = stat.Gid
	}
	return manager, nil
}

func (m *Manager) Enabled() bool { return m != nil && m.enabled }

func (m *Manager) Ensure(ctx context.Context, environmentID, forumID, conversationID uuid.UUID,
	_ string,
) (Runtime, error) {
	if !m.Enabled() {
		return Runtime{}, errors.New("discord 开发容器未启用")
	}
	item, err := m.loadWorkspace(ctx, environmentID, forumID)
	if err != nil {
		return Runtime{}, err
	}
	return m.ensureWorkspace(ctx, environmentID, item)
}

// EnsureProject 为不依赖 Discord Forum 的 Development Session 准备项目运行时。
func (m *Manager) EnsureProject(ctx context.Context, environmentID, projectID uuid.UUID) (Runtime, error) {
	if !m.Enabled() {
		return Runtime{}, errors.New("discord 开发容器未启用")
	}
	item, err := m.loadProject(ctx, environmentID, projectID)
	if err != nil {
		return Runtime{}, err
	}
	return m.ensureWorkspace(ctx, environmentID, item)
}

func (m *Manager) ensureWorkspace(ctx context.Context, environmentID uuid.UUID,
	item workspace,
) (Runtime, error) {
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

	if item.Environment.Status != "running" || item.Environment.ContainerID == "" {
		return Runtime{}, errors.New("长期开发环境尚未运行")
	}
	if item.Status != "ready" {
		return Runtime{}, errors.New("开发项目不可用")
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
		EnvironmentID: environmentID, ForumID: item.ForumID, ProjectID: item.ProjectID,
		Container: item.Environment.ContainerName,
		Workspace: filepath.ToSlash(filepath.Join(containerRoot, item.Relative)), CodexHome: codexHome,
		ProjectKind: item.Kind, RemoteURL: item.CloneURL,
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
		EnvironmentID: environmentID, ForumID: forumID, ProjectID: item.ProjectID,
		Container:   item.Environment.ContainerName,
		Workspace:   filepath.ToSlash(filepath.Join(containerRoot, item.Relative)),
		CodexHome:   filepath.ToSlash(filepath.Join(containerRoot, "codex")),
		ProjectKind: item.Kind, RemoteURL: item.CloneURL,
		User: item.Environment.RuntimeUser, UID: item.Environment.RuntimeUID,
		GID: item.Environment.RuntimeGID, Home: item.Environment.RuntimeHome,
		AppServerSocket: filepath.Join(m.developmentRuntimeDir, environmentID.String(), "app-server.sock"),
		RelaySocket:     filepath.Join(m.developmentRuntimeDir, environmentID.String(), "relay.sock"),
	}, nil
}

func (m *Manager) loadWorkspace(ctx context.Context, environmentID, forumID uuid.UUID) (workspace, error) {
	var item workspace
	var imageRef, imageID, containerID, runtimeUser, runtimeHome sql.NullString
	err := m.db.QueryRowContext(ctx, `SELECT f.id, project.id, project.relative_path, 'ready',
		COALESCE(project.branch,''), project.project_kind, project.name,
		COALESCE(project.remote_url,''), '',
		e.id, e.status, e.image_ref, e.image_id, e.container_name, e.container_id,
		e.data_volume_name, e.home_volume_name, e.network_name, e.runtime_user,
		COALESCE(e.runtime_uid, 0), COALESCE(e.runtime_gid, 0), e.runtime_home
		FROM discord_forums f
		JOIN discord_development_environments e ON e.id=f.development_environment_id
		JOIN development_projects project ON project.id=f.development_project_id
		WHERE f.id=$1 AND e.id=$2 AND f.binding_status='active'
			AND project.availability_status='available'`, forumID, environmentID).Scan(
		&item.ForumID, &item.ProjectID, &item.Relative, &item.Status, &item.Branch, &item.Kind,
		&item.Repository, &item.CloneURL, &item.DefaultRef,
		&item.Environment.ID, &item.Environment.Status,
		&imageRef, &imageID, &item.Environment.ContainerName, &containerID,
		&item.Environment.DataVolume, &item.Environment.HomeVolume, &item.Environment.Network,
		&runtimeUser, &item.Environment.RuntimeUID, &item.Environment.RuntimeGID, &runtimeHome)
	item.Environment.ImageRef, item.Environment.ImageID = imageRef.String, imageID.String
	item.Environment.ContainerID, item.Environment.RuntimeUser = containerID.String, runtimeUser.String
	item.Environment.RuntimeHome = runtimeHome.String
	return item, err
}

func (m *Manager) loadProject(ctx context.Context, environmentID, projectID uuid.UUID) (workspace, error) {
	var item workspace
	var imageRef, imageID, containerID, runtimeUser, runtimeHome sql.NullString
	err := m.db.QueryRowContext(ctx, `SELECT project.id, project.relative_path, 'ready',
		COALESCE(project.branch,''), project.project_kind, project.name,
		COALESCE(project.remote_url,''), '',
		e.id, e.status, e.image_ref, e.image_id, e.container_name, e.container_id,
		e.data_volume_name, e.home_volume_name, e.network_name, e.runtime_user,
		COALESCE(e.runtime_uid, 0), COALESCE(e.runtime_gid, 0), e.runtime_home
		FROM development_projects project
		JOIN discord_development_environments e ON e.id=project.environment_id
		WHERE project.id=$1 AND e.id=$2 AND project.availability_status='available'`,
		projectID, environmentID).Scan(
		&item.ProjectID, &item.Relative, &item.Status, &item.Branch, &item.Kind,
		&item.Repository, &item.CloneURL, &item.DefaultRef,
		&item.Environment.ID, &item.Environment.Status,
		&imageRef, &imageID, &item.Environment.ContainerName, &containerID,
		&item.Environment.DataVolume, &item.Environment.HomeVolume, &item.Environment.Network,
		&runtimeUser, &item.Environment.RuntimeUID, &item.Environment.RuntimeGID, &runtimeHome)
	item.Environment.ImageRef, item.Environment.ImageID = imageRef.String, imageID.String
	item.Environment.ContainerID, item.Environment.RuntimeUser = containerID.String, runtimeUser.String
	item.Environment.RuntimeHome = runtimeHome.String
	return item, err
}

func (m *Manager) docker(ctx context.Context, arguments ...string) (string, error) {
	environment := []string(nil)
	if m.dockerHost != "inherit" {
		environment = []string{"DOCKER_HOST=" + m.dockerHost}
	}
	return m.runner.Run(ctx, environment, "", append([]string{m.dockerBin}, arguments...)...)
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
