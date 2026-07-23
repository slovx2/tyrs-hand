package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/sshconfig"
)

type DevelopmentEnvironment struct {
	ID                 uuid.UUID          `json:"id"`
	OwnerUserID        string             `json:"ownerDiscordUserId"`
	OwnerName          string             `json:"ownerName"`
	BuildRepositoryID  uuid.UUID          `json:"buildRepositoryId"`
	BuildRepository    string             `json:"buildRepository"`
	Status             string             `json:"status"`
	ImageID            string             `json:"imageId,omitempty"`
	SourceSHA          string             `json:"buildSourceSha,omitempty"`
	RuntimeUser        string             `json:"runtimeUser,omitempty"`
	LastUsedAt         time.Time          `json:"lastUsedAt"`
	Error              string             `json:"error,omitempty"`
	ExecutionNodeID    *uuid.UUID         `json:"executionNodeId,omitempty"`
	SSHPublicKey       string             `json:"sshPublicKey,omitempty"`
	SSHFingerprint     string             `json:"sshFingerprint,omitempty"`
	SSHPort            int                `json:"sshPort,omitempty"`
	SSHDiscordUserID   string             `json:"sshDiscordUserId,omitempty"`
	SSHDisplayName     string             `json:"sshDisplayName,omitempty"`
	SSHConfigRevision  int64              `json:"sshConfigRevision"`
	SSHAppliedRevision int64              `json:"sshAppliedRevision"`
	DaemonStatus       string             `json:"daemonStatus"`
	DaemonError        string             `json:"daemonError,omitempty"`
	AppServerStatus    string             `json:"appServerStatus"`
	SSHStatus          string             `json:"sshStatus"`
	RelayStatus        string             `json:"relayStatus"`
	Forums             []DevelopmentForum `json:"forums"`
}

type DevelopmentEnvironmentSSHInput struct {
	PublicKey     string `json:"publicKey"`
	Port          int    `json:"port"`
	DiscordUserID string `json:"discordUserId"`
}

type DevelopmentForum struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	DiscordID    string    `json:"discordId"`
	RepositoryID uuid.UUID `json:"repositoryId"`
	Repository   string    `json:"repository"`
	Status       string    `json:"status"`
	Branch       string    `json:"branch"`
	Dirty        bool      `json:"dirty"`
	Error        string    `json:"error,omitempty"`
}

type DevelopmentDeletePreflight struct {
	ForumID            uuid.UUID `json:"forumId"`
	Dirty              bool      `json:"dirty"`
	Unpushed           bool      `json:"unpushed"`
	Active             bool      `json:"active"`
	DeletesEnvironment bool      `json:"deletesEnvironment"`
	Confirmation       string    `json:"confirmation"`
}

func (m *Manager) DevelopmentEnvironments(ctx context.Context) ([]DevelopmentEnvironment, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT e.id, e.owner_discord_user_id,
		COALESCE(NULLIF(dm.display_name, ''), dm.username), e.build_repository_id, br.owner || '/' || br.name,
		e.status, COALESCE(e.image_id, ''), COALESCE(e.build_source_sha, ''),
		COALESCE(e.runtime_user, ''), e.last_used_at, COALESCE(e.error, ''),
		e.execution_node_id::text, COALESCE(e.ssh_public_key, ''), COALESCE(e.ssh_fingerprint, ''),
		COALESCE(e.ssh_port, 0), COALESCE(e.ssh_discord_user_id, ''),
		COALESCE(NULLIF(ssh_dm.display_name, ''), ssh_dm.username, ''),
		e.ssh_config_revision, e.ssh_applied_revision,
		e.daemon_status, COALESCE(e.daemon_error, ''), e.app_server_status,
		e.ssh_daemon_status, e.relay_status,
		f.id, COALESCE(dr.name, ''), COALESCE(dr.discord_id, ''), f.repository_id,
		COALESCE(r.owner || '/' || r.name, ''), COALESCE(fw.status, ''),
		COALESCE(fw.branch, ''), COALESCE(fw.dirty, false), COALESCE(fw.error, '')
		FROM discord_development_environments e
		JOIN repositories br ON br.id = e.build_repository_id
		JOIN discord_members dm ON dm.guild_id = e.guild_id AND dm.discord_user_id = e.owner_discord_user_id
		LEFT JOIN discord_members ssh_dm ON ssh_dm.guild_id = e.guild_id
			AND ssh_dm.discord_user_id = e.ssh_discord_user_id
		LEFT JOIN discord_forums f ON f.development_environment_id = e.id AND f.forum_type = 'development'
		LEFT JOIN repositories r ON r.id = f.repository_id
		LEFT JOIN discord_resources dr ON dr.id = f.resource_id
		LEFT JOIN discord_forum_workspaces fw ON fw.forum_id = f.id
		ORDER BY lower(dm.display_name), lower(r.owner), lower(r.name), dr.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []DevelopmentEnvironment
	byID := make(map[uuid.UUID]int)
	for rows.Next() {
		var environment DevelopmentEnvironment
		var forumID, executionNodeID sql.NullString
		var forum DevelopmentForum
		if err := rows.Scan(&environment.ID, &environment.OwnerUserID, &environment.OwnerName,
			&environment.BuildRepositoryID, &environment.BuildRepository, &environment.Status, &environment.ImageID,
			&environment.SourceSHA, &environment.RuntimeUser, &environment.LastUsedAt, &environment.Error,
			&executionNodeID, &environment.SSHPublicKey, &environment.SSHFingerprint,
			&environment.SSHPort, &environment.SSHDiscordUserID, &environment.SSHDisplayName,
			&environment.SSHConfigRevision, &environment.SSHAppliedRevision,
			&environment.DaemonStatus, &environment.DaemonError, &environment.AppServerStatus,
			&environment.SSHStatus, &environment.RelayStatus,
			&forumID, &forum.Name, &forum.DiscordID, &forum.RepositoryID, &forum.Repository,
			&forum.Status, &forum.Branch, &forum.Dirty, &forum.Error); err != nil {
			return nil, err
		}
		index, exists := byID[environment.ID]
		if !exists {
			if executionNodeID.Valid {
				id, parseErr := uuid.Parse(executionNodeID.String)
				if parseErr != nil {
					return nil, parseErr
				}
				environment.ExecutionNodeID = &id
			}
			environment.Forums = []DevelopmentForum{}
			result = append(result, environment)
			index = len(result) - 1
			byID[environment.ID] = index
		}
		if forumID.Valid {
			forum.ID, err = uuid.Parse(forumID.String)
			if err != nil {
				return nil, err
			}
			result[index].Forums = append(result[index].Forums, forum)
		}
	}
	return result, rows.Err()
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

func (m *Manager) RebuildDevelopmentEnvironment(ctx context.Context, id uuid.UUID) error {
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
		RETURNING execution_node_id`, id).Scan(&nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("开发环境不存在、未分配节点、正在删除或仍有任务排队/运行")
		}
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO discord_development_operations
		(environment_id, operation, execution_node_id) VALUES ($1, 'rebuild', $2)`, id, nodeID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) DevelopmentForumDeletePreflight(ctx context.Context,
	forumID uuid.UUID,
) (DevelopmentDeletePreflight, error) {
	var result DevelopmentDeletePreflight
	result.ForumID = forumID
	var environmentID uuid.UUID
	var count int
	err := m.db.QueryRowContext(ctx, `SELECT fw.environment_id, fw.dirty,
		COALESCE(fw.head_sha IS DISTINCT FROM fw.base_sha, false),
		EXISTS(SELECT 1 FROM discord_conversations c JOIN codex_turn_intents i
			ON i.discord_conversation_id = c.id WHERE c.forum_id = fw.forum_id
			AND i.status IN ('queued','retry_wait','dispatching','awaiting_confirmation','running','reconciling')),
		(SELECT count(*) FROM discord_forum_workspaces other
			WHERE other.environment_id = fw.environment_id AND other.status <> 'deleting')
		FROM discord_forum_workspaces fw WHERE fw.forum_id = $1 AND fw.status <> 'deleting'`, forumID).
		Scan(&environmentID, &result.Dirty, &result.Unpushed, &result.Active, &count)
	if err != nil {
		return DevelopmentDeletePreflight{}, err
	}
	result.DeletesEnvironment = count == 1
	result.Confirmation = "DELETE " + forumID.String()
	return result, nil
}

func (m *Manager) DeleteDevelopmentForum(ctx context.Context, forumID uuid.UUID,
	confirmation string, administratorID uuid.UUID,
) (uuid.UUID, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var environmentID uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT fw.environment_id FROM discord_forum_workspaces fw
		JOIN discord_development_environments e ON e.id = fw.environment_id
		WHERE fw.forum_id = $1 AND fw.status <> 'deleting' FOR UPDATE OF e`, forumID).Scan(&environmentID)
	if err != nil {
		return uuid.Nil, err
	}
	preflight := DevelopmentDeletePreflight{ForumID: forumID, Confirmation: "DELETE " + forumID.String()}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT fw.dirty,
		COALESCE(fw.head_sha IS DISTINCT FROM fw.base_sha, false),
		EXISTS(SELECT 1 FROM discord_conversations c JOIN codex_turn_intents i
			ON i.discord_conversation_id = c.id WHERE c.forum_id = fw.forum_id
			AND i.status IN ('queued','retry_wait','dispatching','awaiting_confirmation','running','reconciling')),
		(SELECT count(*) FROM discord_forum_workspaces other
			WHERE other.environment_id = fw.environment_id AND other.status <> 'deleting')
		FROM discord_forum_workspaces fw WHERE fw.forum_id = $1 AND fw.status <> 'deleting'`, forumID).
		Scan(&preflight.Dirty, &preflight.Unpushed, &preflight.Active, &count)
	if err != nil {
		return uuid.Nil, err
	}
	preflight.DeletesEnvironment = count == 1
	if confirmation != preflight.Confirmation {
		return uuid.Nil, fmt.Errorf("确认文本必须是 %q", preflight.Confirmation)
	}
	if preflight.Active {
		return uuid.Nil, errors.New("Forum 仍有任务排队或运行，停止或等待任务结束后再删除")
	}
	operation := "delete_forum"
	if preflight.DeletesEnvironment {
		operation = "delete_environment"
	}
	var operationID uuid.UUID
	err = tx.QueryRowContext(ctx, `INSERT INTO discord_development_operations
		(environment_id, forum_id, operation, requested_by, execution_node_id)
		SELECT $1, $2, $3, $4, execution_node_id FROM discord_development_environments
		WHERE id = $1 AND execution_node_id IS NOT NULL RETURNING id`,
		environmentID, forumID, operation, administratorID).Scan(&operationID)
	if err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE discord_forum_workspaces SET status = 'deleting', updated_at = now()
		WHERE forum_id = $1`, forumID); err != nil {
		return uuid.Nil, err
	}
	if preflight.DeletesEnvironment {
		if _, err = tx.ExecContext(ctx, `UPDATE discord_development_environments SET status = 'deleting',
			updated_at = now() WHERE id = $1`, environmentID); err != nil {
			return uuid.Nil, err
		}
	}
	return operationID, tx.Commit()
}
