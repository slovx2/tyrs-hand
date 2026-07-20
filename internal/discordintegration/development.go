package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type DevelopmentEnvironment struct {
	ID                uuid.UUID          `json:"id"`
	OwnerUserID       string             `json:"ownerDiscordUserId"`
	OwnerName         string             `json:"ownerName"`
	BuildRepositoryID uuid.UUID          `json:"buildRepositoryId"`
	BuildRepository   string             `json:"buildRepository"`
	Status            string             `json:"status"`
	ImageID           string             `json:"imageId,omitempty"`
	SourceSHA         string             `json:"buildSourceSha,omitempty"`
	RuntimeUser       string             `json:"runtimeUser,omitempty"`
	LastUsedAt        time.Time          `json:"lastUsedAt"`
	Error             string             `json:"error,omitempty"`
	Forums            []DevelopmentForum `json:"forums"`
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
		f.id, COALESCE(dr.name, ''), COALESCE(dr.discord_id, ''), f.repository_id,
		COALESCE(r.owner || '/' || r.name, ''), COALESCE(fw.status, ''),
		COALESCE(fw.branch, ''), COALESCE(fw.dirty, false), COALESCE(fw.error, '')
		FROM discord_development_environments e
		JOIN repositories br ON br.id = e.build_repository_id
		JOIN discord_members dm ON dm.guild_id = e.guild_id AND dm.discord_user_id = e.owner_discord_user_id
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
		var forumID sql.NullString
		var forum DevelopmentForum
		if err := rows.Scan(&environment.ID, &environment.OwnerUserID, &environment.OwnerName,
			&environment.BuildRepositoryID, &environment.BuildRepository, &environment.Status, &environment.ImageID,
			&environment.SourceSHA, &environment.RuntimeUser, &environment.LastUsedAt, &environment.Error,
			&forumID, &forum.Name, &forum.DiscordID, &forum.RepositoryID, &forum.Repository,
			&forum.Status, &forum.Branch, &forum.Dirty, &forum.Error); err != nil {
			return nil, err
		}
		index, exists := byID[environment.ID]
		if !exists {
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

func (m *Manager) RebuildDevelopmentEnvironment(ctx context.Context, id uuid.UUID) error {
	result, err := m.db.ExecContext(ctx, `UPDATE discord_development_environments
		SET status = 'pending', error = NULL, updated_at = now()
		WHERE id = $1 AND status <> 'deleting' AND NOT EXISTS (
			SELECT 1 FROM discord_forums f JOIN discord_conversations c ON c.forum_id = f.id
			JOIN codex_turn_intents i ON i.discord_conversation_id = c.id
			WHERE f.development_environment_id = $1
			AND i.status IN ('dispatching','awaiting_confirmation','running','reconciling'))`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("开发环境不存在、正在删除或仍有任务运行")
	}
	return nil
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
		(environment_id, forum_id, operation, requested_by) VALUES ($1, $2, $3, $4) RETURNING id`,
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
