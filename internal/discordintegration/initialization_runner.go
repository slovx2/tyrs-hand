package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	disgorest "github.com/disgoorg/disgo/rest"
	"github.com/google/uuid"
)

const initializationMaxAttempts = 3

func (m *Manager) RunInitialization(ctx context.Context, operationID uuid.UUID, remote Remote) error {
	var guildID string
	err := m.db.QueryRowContext(ctx, `UPDATE discord_initialization_operations SET status = 'running',
		started_at = COALESCE(started_at, now()), error = NULL, updated_at = now()
		WHERE id = $1 AND status <> 'completed' RETURNING guild_id`, operationID).Scan(&guildID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for {
		stepKey, action, err := m.claimInitializationStep(ctx, operationID)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = m.db.ExecContext(ctx, `UPDATE discord_initialization_operations SET status = 'completed',
				finished_at = now(), updated_at = now() WHERE id = $1`, operationID)
			return err
		}
		if err != nil {
			return m.failInitialization(ctx, operationID, "", err)
		}
		result, executeErr := m.executeInitializationAction(ctx, guildID, action, remote)
		if executeErr != nil {
			return m.failInitialization(ctx, operationID, stepKey, executeErr)
		}
		encoded, _ := json.Marshal(result)
		_, err = m.db.ExecContext(ctx, `UPDATE discord_initialization_steps SET status = 'completed',
			result = $3, error = NULL, finished_at = now() WHERE operation_id = $1 AND step_key = $2`,
			operationID, stepKey, encoded)
		if err != nil {
			return m.failInitialization(ctx, operationID, stepKey, err)
		}
	}
}

func (m *Manager) claimInitializationStep(ctx context.Context, operationID uuid.UUID) (string, InitializationAction, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return "", InitializationAction{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var key string
	var raw []byte
	var attemptCount int
	err = tx.QueryRowContext(ctx, `SELECT step_key, request, attempt_count FROM discord_initialization_steps
		WHERE operation_id = $1 AND status <> 'completed' ORDER BY ordinal FOR UPDATE SKIP LOCKED LIMIT 1`, operationID).
		Scan(&key, &raw, &attemptCount)
	if err != nil {
		return "", InitializationAction{}, err
	}
	if attemptCount >= initializationMaxAttempts {
		return "", InitializationAction{}, errors.New("Discord 初始化步骤重试次数已耗尽")
	}
	_, err = tx.ExecContext(ctx, `UPDATE discord_initialization_steps SET status = 'running',
		attempt_count = attempt_count + 1, started_at = COALESCE(started_at, now()), error = NULL
		WHERE operation_id = $1 AND step_key = $2`, operationID, key)
	if err != nil {
		return "", InitializationAction{}, err
	}
	var action InitializationAction
	if err := json.Unmarshal(raw, &action); err != nil {
		return "", InitializationAction{}, err
	}
	return key, action, tx.Commit()
}

func (m *Manager) executeInitializationAction(ctx context.Context, guildID string, action InitializationAction, remote Remote) (map[string]any, error) {
	switch action.Kind {
	case "community.disable":
		return nil, remote.DisableCommunity(ctx, guildID)
	case "community.enable":
		rules, err := m.managedResourceID(ctx, guildID, "system.rules")
		if err != nil {
			return nil, err
		}
		updates, err := m.managedResourceID(ctx, guildID, "system.updates")
		if err != nil {
			return nil, err
		}
		return nil, remote.EnableCommunity(ctx, guildID, rules, updates)
	case "channel.delete":
		err := remote.DeleteChannel(ctx, action.ResourceID)
		if err != nil && !isRemoteStatus(err, 404) {
			return nil, err
		}
		_, dbErr := m.db.ExecContext(ctx, "DELETE FROM discord_resources WHERE guild_id = $1 AND discord_id = $2", guildID, action.ResourceID)
		return nil, dbErr
	case "channel.create":
		return m.createManagedChannel(ctx, guildID, action.Spec, remote)
	case "channel.update":
		spec, err := m.resolveChannelSpec(ctx, guildID, action.Spec)
		if err != nil {
			return nil, err
		}
		if err := remote.UpdateChannel(ctx, action.ResourceID, spec); err != nil {
			return nil, err
		}
		_, err = m.db.ExecContext(ctx, `UPDATE discord_resources SET name = $3, kind = $4,
			parent_discord_id = NULLIF($5, ''), managed_marker = $6, updated_at = now()
			WHERE guild_id = $1 AND discord_id = $2`, guildID, action.ResourceID,
			spec.Name, spec.Kind, spec.ParentKey, managedMarker(spec.Key))
		return nil, err
	case "forum.personal.record":
		var resourceID uuid.UUID
		err := m.db.QueryRowContext(ctx, `SELECT id FROM discord_resources
			WHERE guild_id = $1 AND resource_key = $2 AND status = 'active'`, guildID, action.Spec.Key).Scan(&resourceID)
		if err != nil {
			return nil, err
		}
		_, err = m.db.ExecContext(ctx, `INSERT INTO discord_forums
			(guild_id, resource_id, forum_type, owner_discord_user_id)
			VALUES ($1, $2, 'personal', $3) ON CONFLICT(resource_id) DO NOTHING`,
			guildID, resourceID, action.OwnerUserID)
		return nil, err
	case "forum.repository.record":
		var resourceID uuid.UUID
		err := m.db.QueryRowContext(ctx, `SELECT id FROM discord_resources
			WHERE guild_id = $1 AND resource_key = $2 AND status = 'active'`, guildID, action.Spec.Key).Scan(&resourceID)
		if err != nil {
			return nil, err
		}
		repositoryID, err := uuid.Parse(action.RepositoryID)
		if err != nil {
			return nil, err
		}
		_, err = m.db.ExecContext(ctx, `INSERT INTO discord_forums
			(guild_id, resource_id, forum_type, repository_id)
			VALUES ($1, $2, 'repository', $3) ON CONFLICT(resource_id) DO NOTHING`, guildID, resourceID, repositoryID)
		return nil, err
	default:
		return nil, fmt.Errorf("未知初始化步骤 %q", action.Kind)
	}
}

func (m *Manager) createManagedChannel(ctx context.Context, guildID string, input ChannelSpec, remote Remote) (map[string]any, error) {
	spec, err := m.resolveChannelSpec(ctx, guildID, input)
	if err != nil {
		return nil, err
	}
	marker := managedMarker(spec.Key)
	guild, err := remote.Guild(ctx, guildID)
	if err != nil {
		return nil, err
	}
	var channel RemoteChannel
	for _, candidate := range guild.Channels {
		managedMatch := strings.Contains(candidate.Topic, marker)
		if spec.Kind == "category" {
			// Category 没有 Topic；预检已保证同名资源无冲突，因此可用名称和类型恢复模糊成功。
			managedMatch = candidate.Name == spec.Name
		}
		if candidate.Name == spec.Name && candidate.Kind == spec.Kind && managedMatch {
			channel = candidate
			break
		}
	}
	if channel.ID == "" {
		channel, err = remote.CreateChannel(ctx, guildID, spec, marker)
		if err != nil {
			return nil, err
		}
	}
	_, err = m.db.ExecContext(ctx, `INSERT INTO discord_resources
		(guild_id, resource_key, discord_id, kind, parent_discord_id, name, managed_marker)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7)
		ON CONFLICT(guild_id, resource_key) DO UPDATE SET discord_id = EXCLUDED.discord_id,
			kind = EXCLUDED.kind, parent_discord_id = EXCLUDED.parent_discord_id,
			name = EXCLUDED.name, managed_marker = EXCLUDED.managed_marker, status = 'active', updated_at = now()`,
		guildID, spec.Key, channel.ID, spec.Kind, spec.ParentKey, spec.Name, marker)
	if err != nil {
		return nil, err
	}
	return map[string]any{"discordId": channel.ID, "resourceKey": spec.Key}, nil
}

func (m *Manager) resolveChannelSpec(ctx context.Context, guildID string, spec ChannelSpec) (ChannelSpec, error) {
	if spec.ParentKey != "" && !validSnowflake(spec.ParentKey) {
		parentID, err := m.managedResourceID(ctx, guildID, spec.ParentKey)
		if err != nil {
			return ChannelSpec{}, err
		}
		spec.ParentKey = parentID
	}
	if spec.Kind != "category" {
		spec.Topic = managedTopic(spec.Topic, managedMarker(spec.Key))
	}
	return spec, nil
}

func (m *Manager) managedResourceID(ctx context.Context, guildID, key string) (string, error) {
	var id string
	err := m.db.QueryRowContext(ctx, `SELECT discord_id FROM discord_resources
		WHERE guild_id = $1 AND resource_key = $2 AND status = 'active'`, guildID, key).Scan(&id)
	return id, err
}

func (m *Manager) failInitialization(ctx context.Context, operationID uuid.UUID, stepKey string, cause error) error {
	if stepKey != "" {
		_, _ = m.db.ExecContext(ctx, `UPDATE discord_initialization_steps SET status = 'failed', error = $3
			WHERE operation_id = $1 AND step_key = $2`, operationID, stepKey, cause.Error())
	}
	_, _ = m.db.ExecContext(ctx, `UPDATE discord_initialization_operations SET status = 'failed',
		error = $2, updated_at = now() WHERE id = $1`, operationID, cause.Error())
	return cause
}

func isRemoteStatus(err error, status int) bool {
	var remoteErr *disgorest.Error
	return errors.As(err, &remoteErr) && remoteErr.Response != nil && remoteErr.Response.StatusCode == status
}
