package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/sshconfig"
)

type DevelopmentEnvironment struct {
	ID                 uuid.UUID            `json:"id"`
	OwnerUserID        string               `json:"ownerDiscordUserId"`
	OwnerName          string               `json:"ownerName"`
	Status             string               `json:"status"`
	ImageRef           string               `json:"imageRef"`
	ImageID            string               `json:"imageId,omitempty"`
	RuntimeUser        string               `json:"runtimeUser,omitempty"`
	CodexVersion       string               `json:"codexVersion,omitempty"`
	CodexUserOverride  bool                 `json:"codexUserOverride"`
	LastUsedAt         time.Time            `json:"lastUsedAt"`
	Error              string               `json:"error,omitempty"`
	ExecutionNodeID    *uuid.UUID           `json:"executionNodeId,omitempty"`
	SSHPublicKey       string               `json:"sshPublicKey,omitempty"`
	SSHFingerprint     string               `json:"sshFingerprint,omitempty"`
	SSHPort            int                  `json:"sshPort,omitempty"`
	SSHDiscordUserID   string               `json:"sshDiscordUserId,omitempty"`
	SSHDisplayName     string               `json:"sshDisplayName,omitempty"`
	SSHConfigRevision  int64                `json:"sshConfigRevision"`
	SSHAppliedRevision int64                `json:"sshAppliedRevision"`
	DaemonStatus       string               `json:"daemonStatus"`
	DaemonError        string               `json:"daemonError,omitempty"`
	AppServerStatus    string               `json:"appServerStatus"`
	SSHStatus          string               `json:"sshStatus"`
	RelayStatus        string               `json:"relayStatus"`
	ProjectsScannedAt  *time.Time           `json:"projectsScannedAt,omitempty"`
	ProjectScanError   string               `json:"projectScanError,omitempty"`
	Projects           []DevelopmentProject `json:"projects"`
}

type DevelopmentEnvironmentSSHInput struct {
	PublicKey     string `json:"publicKey"`
	Port          int    `json:"port"`
	DiscordUserID string `json:"discordUserId"`
}

type DevelopmentForum struct {
	ID            uuid.UUID     `json:"id"`
	Name          string        `json:"name"`
	DiscordID     string        `json:"discordId"`
	BindingStatus string        `json:"bindingStatus"`
	Collaborators []ForumAccess `json:"collaborators"`
}

type DevelopmentProject struct {
	ID                  uuid.UUID          `json:"id"`
	Name                string             `json:"name"`
	RelativePath        string             `json:"relativePath"`
	DesiredRelativePath string             `json:"desiredRelativePath,omitempty"`
	ProjectKind         string             `json:"projectKind"`
	AvailabilityStatus  string             `json:"availabilityStatus"`
	Branch              string             `json:"branch,omitempty"`
	HeadSHA             string             `json:"headSha,omitempty"`
	Dirty               bool               `json:"dirty"`
	RemoteURL           string             `json:"remoteUrl,omitempty"`
	LastSeenAt          time.Time          `json:"lastSeenAt"`
	ScanError           string             `json:"scanError,omitempty"`
	Forums              []DevelopmentForum `json:"forums"`
}

func (m *Manager) DevelopmentEnvironments(ctx context.Context) ([]DevelopmentEnvironment, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT e.id, e.owner_discord_user_id,
		COALESCE(NULLIF(dm.display_name, ''), dm.username), e.status, COALESCE(e.image_ref, ''),
		COALESCE(e.image_id, ''), COALESCE(e.runtime_user, ''), COALESCE(e.codex_version, ''),
		e.codex_user_override, e.last_used_at, COALESCE(e.error, ''),
		e.execution_node_id::text, COALESCE(e.ssh_public_key, ''), COALESCE(e.ssh_fingerprint, ''),
		COALESCE(e.ssh_port, 0), COALESCE(e.ssh_discord_user_id, ''),
		COALESCE(NULLIF(ssh_dm.display_name, ''), ssh_dm.username, ''),
		e.ssh_config_revision, e.ssh_applied_revision,
		e.daemon_status, COALESCE(e.daemon_error, ''), e.app_server_status,
		e.ssh_daemon_status, e.relay_status, e.projects_scanned_at,
		COALESCE(e.project_scan_error,'')
		FROM discord_development_environments e
		JOIN discord_members dm ON dm.guild_id = e.guild_id AND dm.discord_user_id = e.owner_discord_user_id
		LEFT JOIN discord_members ssh_dm ON ssh_dm.guild_id = e.guild_id
			AND ssh_dm.discord_user_id = e.ssh_discord_user_id
		ORDER BY lower(COALESCE(NULLIF(dm.display_name,''),dm.username)), e.created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]DevelopmentEnvironment, 0)
	for rows.Next() {
		var environment DevelopmentEnvironment
		var executionNodeID sql.NullString
		var projectsScannedAt sql.NullTime
		if err := rows.Scan(&environment.ID, &environment.OwnerUserID, &environment.OwnerName,
			&environment.Status, &environment.ImageRef, &environment.ImageID,
			&environment.RuntimeUser, &environment.CodexVersion, &environment.CodexUserOverride,
			&environment.LastUsedAt, &environment.Error,
			&executionNodeID, &environment.SSHPublicKey, &environment.SSHFingerprint,
			&environment.SSHPort, &environment.SSHDiscordUserID, &environment.SSHDisplayName,
			&environment.SSHConfigRevision, &environment.SSHAppliedRevision,
			&environment.DaemonStatus, &environment.DaemonError, &environment.AppServerStatus,
			&environment.SSHStatus, &environment.RelayStatus, &projectsScannedAt,
			&environment.ProjectScanError); err != nil {
			return nil, err
		}
		if executionNodeID.Valid {
			id, parseErr := uuid.Parse(executionNodeID.String)
			if parseErr != nil {
				return nil, parseErr
			}
			environment.ExecutionNodeID = &id
		}
		if projectsScannedAt.Valid {
			environment.ProjectsScannedAt = &projectsScannedAt.Time
		}
		environment.Projects = []DevelopmentProject{}
		result = append(result, environment)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range result {
		projects, err := m.developmentProjects(ctx, result[index].ID)
		if err != nil {
			return nil, err
		}
		result[index].Projects = projects
	}
	return result, nil
}

func (m *Manager) developmentProjects(ctx context.Context,
	environmentID uuid.UUID,
) ([]DevelopmentProject, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT project.id, project.name,
		project.relative_path, COALESCE(project.desired_relative_path,''),
		project.project_kind, project.availability_status, COALESCE(project.branch,''),
		COALESCE(project.head_sha,''), project.dirty, COALESCE(project.remote_url,''),
		project.last_seen_at, COALESCE(project.scan_error,''),
		forum.id::text, COALESCE(resource.name,''), COALESCE(resource.discord_id,''),
		COALESCE(forum.binding_status,'')
		FROM development_projects project
		LEFT JOIN discord_forums forum ON forum.development_project_id=project.id
			AND forum.forum_type='development'
		LEFT JOIN discord_resources resource ON resource.id=forum.resource_id
		WHERE project.environment_id=$1
		ORDER BY lower(project.name), project.relative_path, forum.created_at`,
		environmentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]DevelopmentProject, 0)
	byID := make(map[uuid.UUID]int)
	for rows.Next() {
		var project DevelopmentProject
		var forumID sql.NullString
		var forum DevelopmentForum
		if err := rows.Scan(&project.ID, &project.Name, &project.RelativePath,
			&project.DesiredRelativePath, &project.ProjectKind, &project.AvailabilityStatus,
			&project.Branch, &project.HeadSHA, &project.Dirty, &project.RemoteURL,
			&project.LastSeenAt, &project.ScanError, &forumID, &forum.Name,
			&forum.DiscordID, &forum.BindingStatus); err != nil {
			return nil, err
		}
		index, exists := byID[project.ID]
		if !exists {
			project.Forums = []DevelopmentForum{}
			result = append(result, project)
			index = len(result) - 1
			byID[project.ID] = index
		}
		if forumID.Valid {
			forum.ID, err = uuid.Parse(forumID.String)
			if err != nil {
				return nil, err
			}
			forum.Collaborators, err = m.ForumAccess(ctx, forum.ID)
			if err != nil {
				return nil, err
			}
			result[index].Forums = append(result[index].Forums, forum)
		}
	}
	return result, rows.Err()
}

func (m *Manager) CreateDevelopmentEnvironment(ctx context.Context, ownerID string,
	administratorID uuid.UUID,
) (uuid.UUID, uuid.UUID, error) {
	if m.developmentImage == "" {
		return uuid.Nil, uuid.Nil, errors.New("尚未配置 TYRS_HAND_DEVELOPMENT_IMAGE")
	}
	settings, err := m.Settings(ctx)
	if err != nil || settings.GuildID == "" {
		return uuid.Nil, uuid.Nil, errors.New("Discord Guild 尚未配置")
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM discord_members member
		WHERE member.guild_id=$1 AND member.discord_user_id=$2 AND member.active
		  AND NOT EXISTS (
			SELECT 1 FROM discord_development_environments environment
			WHERE environment.guild_id=member.guild_id
			  AND environment.owner_discord_user_id=member.discord_user_id))`,
		settings.GuildID, ownerID).Scan(&eligible); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if !eligible {
		return uuid.Nil, uuid.Nil, errors.New("成员不活跃或已经拥有长期开发环境")
	}
	environmentID := uuid.New()
	compact := strings.ReplaceAll(environmentID.String(), "-", "")
	var nodeID uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT node.id
		FROM platform_settings setting
		JOIN execution_nodes node ON node.id=(setting.value->>'nodeId')::uuid
		WHERE setting.setting_key='execution.default.discord'
		  AND node.enabled AND node.roles ? 'discord'`).Scan(&nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, uuid.Nil, errors.New("尚未配置可用的 Discord 默认执行节点")
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO discord_development_environments
		(id,guild_id,owner_discord_user_id,image_ref,container_name,data_volume_name,
			home_volume_name,network_name,execution_node_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		environmentID, settings.GuildID, ownerID, m.developmentImage,
		"tyrs-hand-dev-"+compact, "tyrs-hand-dev-data-"+compact,
		"tyrs-hand-dev-home-"+compact, "tyrs-hand-dev-net-"+compact, nodeID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	var operationID uuid.UUID
	err = tx.QueryRowContext(ctx, `INSERT INTO discord_development_operations
		(environment_id,operation,requested_by,execution_node_id)
		VALUES ($1,'provision_environment',$2,$3) RETURNING id`,
		environmentID, administratorID, nodeID).Scan(&operationID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return environmentID, operationID, tx.Commit()
}

func (m *Manager) SaveDevelopmentEnvironmentSSH(ctx context.Context, id uuid.UUID,
	input DevelopmentEnvironmentSSHInput,
) (string, error) {
	if input.Port < 1 || input.Port > 65535 {
		return "", errors.New("SSH 端口必须在 1 到 65535 之间")
	}
	if !validSnowflake(input.DiscordUserID) {
		return "", errors.New("SSH 必须绑定有效的 Discord 成员")
	}
	publicKey, fingerprint, err := sshconfig.ParseAuthorizedPublicKey(input.PublicKey)
	if err != nil {
		return "", err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var memberExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM discord_development_environments e
		JOIN discord_members m ON m.guild_id = e.guild_id
			AND m.discord_user_id = $2 AND m.active = true
		WHERE e.id = $1 AND e.status <> 'deleting')`, id, input.DiscordUserID).
		Scan(&memberExists); err != nil {
		return "", err
	}
	if !memberExists {
		return "", errors.New("SSH 绑定用户不是当前 Guild 的活跃 Discord 成员")
	}
	var nodeID uuid.UUID
	var revision int64
	err = tx.QueryRowContext(ctx, `UPDATE discord_development_environments SET
		ssh_public_key = $2, ssh_fingerprint = $3, ssh_port = $4,
		ssh_discord_user_id = $5,
		ssh_config_revision = ssh_config_revision + 1, daemon_status = 'pending',
		daemon_error = NULL, updated_at = now()
		WHERE id = $1 AND status <> 'deleting' AND execution_node_id IS NOT NULL
		RETURNING execution_node_id, ssh_config_revision`, id, publicKey, fingerprint, input.Port,
		input.DiscordUserID).
		Scan(&nodeID, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("开发环境不存在、正在删除或尚未分配执行节点")
	}
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO discord_development_operations
		(environment_id, operation, execution_node_id)
		SELECT $1, 'reconfigure', $2 WHERE NOT EXISTS (
			SELECT 1 FROM discord_development_operations
			WHERE environment_id = $1 AND operation = 'reconfigure' AND status IN ('pending','running')
		)`, id, nodeID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return fingerprint, nil
}

func (m *Manager) ClearDevelopmentEnvironmentSSH(ctx context.Context, id uuid.UUID) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var nodeID uuid.UUID
	err = tx.QueryRowContext(ctx, `UPDATE discord_development_environments SET
		ssh_public_key = NULL, ssh_fingerprint = NULL, ssh_port = NULL,
		ssh_discord_user_id = NULL,
		ssh_config_revision = ssh_config_revision + 1, daemon_status = 'pending',
		daemon_error = NULL, updated_at = now()
		WHERE id = $1 AND status <> 'deleting' AND execution_node_id IS NOT NULL
		RETURNING execution_node_id`, id).Scan(&nodeID)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO discord_development_operations
		(environment_id, operation, execution_node_id)
		SELECT $1, 'reconfigure', $2 WHERE NOT EXISTS (
			SELECT 1 FROM discord_development_operations
			WHERE environment_id = $1 AND operation = 'reconfigure' AND status IN ('pending','running')
		)`, id, nodeID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) RebaseDevelopmentEnvironment(ctx context.Context, id uuid.UUID) error {
	if m.developmentImage == "" {
		return errors.New("Control 未配置 TYRS_HAND_DEVELOPMENT_IMAGE")
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var nodeID uuid.UUID
	err = tx.QueryRowContext(ctx, `UPDATE discord_development_environments
		SET status = 'building', error = NULL, updated_at = now()
		WHERE id = $1 AND status <> 'deleting' AND execution_node_id IS NOT NULL
		AND NOT EXISTS (
			SELECT 1 FROM discord_forums f JOIN discord_conversations c ON c.forum_id = f.id
			JOIN codex_turn_intents i ON i.discord_conversation_id = c.id
			WHERE f.development_environment_id = $1
			AND i.status IN ('queued','retry_wait','dispatching','awaiting_confirmation','running','reconciling'))
		AND NOT EXISTS (
			SELECT 1 FROM codex_thread_controls ct JOIN codex_turn_runs r ON r.control_id = ct.id
			WHERE ct.development_environment_id = $1
			AND r.status IN ('starting','running','waiting_for_user','reconciling'))
		RETURNING execution_node_id`, id).Scan(&nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("开发环境不存在、未分配节点、正在删除或仍有任务排队/运行")
		}
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO discord_development_operations
		(environment_id, operation, execution_node_id) VALUES ($1, 'rebase', $2)`, id, nodeID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
