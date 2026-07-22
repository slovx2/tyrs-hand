package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
)

type desktopPostResult struct {
	ThreadID  string `json:"threadId"`
	MessageID string `json:"messageId"`
}

type desktopPostConfig struct {
	Model           string
	ReasoningEffort string
	ServiceTier     string
}

func (s *SQLoutbox) completeDesktopThreadPost(ctx context.Context, tx *sql.Tx,
	item OutboxItem, response json.RawMessage,
) error {
	requestID, err := uuid.Parse(strings.TrimPrefix(item.OperationKey, "desktop-thread-post:"))
	if err != nil {
		return errors.New("desktop thread Post operation key 无效")
	}
	var result desktopPostResult
	if json.Unmarshal(response, &result) != nil || result.ThreadID == "" || result.MessageID == "" {
		return errors.New("desktop thread Post Outbox 结果无效")
	}
	var status, operation, guildID, ownerID, repositoryName string
	var environmentID, forumID, repositoryID, profileID, executionNodeID uuid.UUID
	var sourceControl sql.NullString
	var requestParams json.RawMessage
	err = tx.QueryRowContext(ctx, `SELECT r.status, r.operation, r.environment_id, r.forum_id,
		r.source_control_id::text, r.request_params, f.guild_id, f.owner_discord_user_id,
		f.repository_id, e.execution_node_id, repo.owner || '/' || repo.name
		FROM desktop_thread_requests r JOIN discord_forums f ON f.id = r.forum_id
		JOIN discord_development_environments e ON e.id = r.environment_id
		JOIN repositories repo ON repo.id = f.repository_id
		WHERE r.id = $1 FOR UPDATE`, requestID).Scan(&status, &operation, &environmentID,
		&forumID, &sourceControl, &requestParams, &guildID, &ownerID, &repositoryID,
		&executionNodeID, &repositoryName)
	if err != nil {
		return err
	}
	if status == "codex_pending" || status == "completed" {
		return nil
	}
	if status != "post_pending" {
		return errors.New("desktop thread Post reservation 状态无效")
	}
	var contextVersion int64
	var config desktopPostConfig
	if sourceControl.Valid {
		err = tx.QueryRowContext(ctx, `SELECT agent_profile_id, context_version,
			COALESCE(model,''), COALESCE(reasoning_effort,''), COALESCE(service_tier,'standard')
			FROM codex_thread_controls WHERE id = $1 AND development_environment_id = $2`,
			sourceControl.String, environmentID).Scan(&profileID, &contextVersion,
			&config.Model, &config.ReasoningEffort, &config.ServiceTier)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT id, context_version FROM agent_profiles
			ORDER BY created_at, id LIMIT 1`).Scan(&profileID, &contextVersion)
	}
	if err != nil {
		return err
	}
	if !sourceControl.Valid {
		preferences, resolveErr := codexsettings.NewService(s.db).Resolve(ctx, repositoryID,
			forumID, profileID)
		if resolveErr != nil {
			return resolveErr
		}
		config.Model, config.ReasoningEffort, config.ServiceTier = preferences.Model,
			preferences.ReasoningEffort, preferences.ServiceTier
		applyDesktopPostParams(&config, requestParams)
	}
	if config.ServiceTier != "fast" {
		config.ServiceTier = "standard"
	}
	conversationID := uuid.New()
	_, err = tx.ExecContext(ctx, `INSERT INTO discord_conversations
		(id, guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
		 repository_id, agent_profile_id, title, status, model, reasoning_effort, service_tier,
		 configuration_status, configured_by_discord_user_id, title_rename_status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'active',NULLIF($10,''),NULLIF($11,''),$12,
			'configured',$6,'skipped')`, conversationID, guildID, forumID, result.ThreadID,
		result.MessageID, ownerID, repositoryID, profileID, "Codex Desktop · "+repositoryName,
		config.Model, config.ReasoningEffort, config.ServiceTier)
	if err != nil {
		return err
	}
	controlID := uuid.New()
	_, err = tx.ExecContext(ctx, `INSERT INTO codex_thread_controls
		(id, source_type, discord_conversation_id, repository_id, agent_profile_id,
		 context_version, execution_node_id, development_environment_id, model,
		 reasoning_effort, service_tier, runtime_preferences_frozen_at, codex_home_key)
		VALUES ($1,'discord_conversation',$2,$3,$4,$5,$6,$7::uuid,NULLIF($8,''),NULLIF($9,''),$10,
			now(),($7::uuid)::text)`, controlID, conversationID, repositoryID, profileID, contextVersion,
		executionNodeID, environmentID, config.Model, config.ReasoningEffort, config.ServiceTier)
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE desktop_thread_requests SET
		status = 'codex_pending', conversation_id = $2, control_id = $3, updated_at = now()
		WHERE id = $1 AND status = 'post_pending'`, requestID, conversationID, controlID)
	if err != nil {
		return err
	}
	changed, _ := updated.RowsAffected()
	if changed != 1 {
		return errors.New("desktop thread Post reservation 已被并发修改")
	}
	_ = operation
	return nil
}

func applyDesktopPostParams(config *desktopPostConfig, params json.RawMessage) {
	var value struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"effort"`
		ServiceTier     string `json:"serviceTier"`
	}
	if json.Unmarshal(params, &value) != nil {
		return
	}
	if value.Model != "" {
		config.Model = value.Model
	}
	if value.ReasoningEffort != "" {
		config.ReasoningEffort = value.ReasoningEffort
	}
	if value.ServiceTier == "standard" || value.ServiceTier == "fast" {
		config.ServiceTier = value.ServiceTier
	}
}
