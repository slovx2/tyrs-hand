package discordintegration

import (
	"context"
	"database/sql"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

var discordSecretPattern = regexp.MustCompile(`(?i)\b(?:sk|ghp|github_pat)_[a-z0-9_-]{12,}\b|\bBearer\s+[a-z0-9._~+/-]{12,}`)

func SanitizeDiscordResult(value string) string {
	value = strings.TrimSpace(codexcontrol.RenderableFinalAnswer(value))
	return discordSecretPattern.ReplaceAllString(value, "[已隐藏凭据]")
}

// ProjectConversationConfiguration 只投影创建 Post 时的设置卡。
// 会话开始后，执行状态完全来自官方 Thread/Turn/Item 投影。
func ProjectConversationConfiguration(ctx context.Context, db *sql.DB, guildID, threadID string,
	conversationID uuid.UUID, inputMessageID string,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var state ConversationModeState
	err = tx.QueryRowContext(ctx, `SELECT id, collaboration_mode, collaboration_mode_revision,
		trigger_mode, trigger_mode_revision, COALESCE(model,''), COALESCE(reasoning_effort,''),
		COALESCE(service_tier,'standard'), settings_revision
		FROM discord_conversations WHERE id = $1 AND guild_id = $2 AND thread_id = $3`,
		conversationID, guildID, threadID).Scan(&state.ConversationID, &state.Mode, &state.Revision,
		&state.TriggerMode, &state.TriggerRevision, &state.Model, &state.ReasoningEffort,
		&state.ServiceTier, &state.SettingsRevision)
	if err != nil {
		return err
	}
	state.Awaiting = true
	if err = upsertWaitingConfigurationProjectionTx(ctx, tx, guildID, threadID,
		inputMessageID, state); err != nil {
		return err
	}
	return tx.Commit()
}

func refreshWaitingConfigurationProjectionTx(ctx context.Context, tx *sql.Tx,
	state ConversationModeState,
) error {
	if !state.Awaiting {
		return nil
	}
	var guildID, threadID, starterID string
	err := tx.QueryRowContext(ctx, `SELECT guild_id, thread_id, COALESCE(starter_message_id,'')
		FROM discord_conversations WHERE id = $1`, state.ConversationID).
		Scan(&guildID, &threadID, &starterID)
	if err != nil {
		return err
	}
	return upsertWaitingConfigurationProjectionTx(ctx, tx, guildID, threadID, starterID, state)
}

func upsertWaitingConfigurationProjectionTx(ctx context.Context, tx *sql.Tx,
	guildID, threadID, inputMessageID string, state ConversationModeState,
) error {
	card := conversationModeCard(state, "")
	key := "conversation:" + state.ConversationID.String() + ":message:" + inputMessageID
	payload := map[string]any{"card": card}
	var messageID string
	err := tx.QueryRowContext(ctx, `INSERT INTO discord_projections
		(guild_id, projection_key, resource_id, desired_payload) VALUES ($1,$2,$3,$4)
		ON CONFLICT(guild_id, projection_key) DO UPDATE SET
		resource_id = EXCLUDED.resource_id, desired_payload = EXCLUDED.desired_payload,
		desired_version = discord_projections.desired_version + 1, updated_at = now()
		RETURNING COALESCE(message_id,'')`, guildID, key, threadID, mustJSON(payload)).Scan(&messageID)
	if err != nil {
		return err
	}
	operationType, nonce := "message.create", "conversation-config-"+state.ConversationID.String()
	payload["channelId"] = threadID
	if messageID != "" {
		operationType, nonce = "message.update", ""
		payload["messageId"] = messageID
	}
	return enqueueDiscordOutbox(ctx, tx, "projection:"+key, operationType,
		"channels/"+threadID+"/messages", payload, nonce)
}
