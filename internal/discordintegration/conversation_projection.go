package discordintegration

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	discordSecretPattern = regexp.MustCompile(`(?i)\b(?:sk|ghp|github_pat)_[a-z0-9_-]{12,}\b|\bBearer\s+[a-z0-9._~+/-]{12,}`)
	discordPathPattern   = regexp.MustCompile(`(?m)(^|[\s("'])/(?:Users|home|root|tmp|var|Volumes|workspace|data)(?:/[^\s"',，。；;、]*)?`)
)

func SanitizeDiscordResult(value string) string {
	value = strings.TrimSpace(value)
	value = discordSecretPattern.ReplaceAllString(value, "[已隐藏凭据]")
	value = discordPathPattern.ReplaceAllString(value, "$1[已隐藏路径]")
	const maxRunes = 1900
	if utf8.RuneCountInString(value) > maxRunes {
		runes := []rune(value)
		value = string(runes[:maxRunes]) + "\n\n（内容已截断）"
	}
	return value
}

func ProjectConversationStatus(ctx context.Context, db *sql.DB, guildID, threadID string,
	conversationID uuid.UUID, inputMessageID string, state ConversationProgress, detail string,
) error {
	card := conversationProgressCard(state, detail)
	key := "conversation:" + conversationID.String() + ":message:" + inputMessageID
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var resourceID, messageID string
	err = tx.QueryRowContext(ctx, `INSERT INTO discord_projections
		(guild_id, projection_key, resource_id, desired_payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT(guild_id, projection_key) DO UPDATE SET
			resource_id = EXCLUDED.resource_id, desired_payload = EXCLUDED.desired_payload,
			desired_version = discord_projections.desired_version + 1, updated_at = now()
		RETURNING resource_id, COALESCE(message_id, '')`, guildID, key, threadID,
		mustJSON(map[string]any{"content": "", "embeds": []EmbedPayload{card}})).Scan(&resourceID, &messageID)
	if err != nil {
		return err
	}
	operationType := "message.create"
	payload := map[string]any{"channelId": resourceID, "content": "", "embeds": []EmbedPayload{card}}
	nonce := "conversation-status-" + conversationID.String() + "-" + inputMessageID
	if messageID != "" {
		operationType = "message.update"
		payload["messageId"] = messageID
		nonce = ""
	}
	if err := enqueueDiscordOutbox(ctx, tx, "projection:"+key, operationType,
		"channels/"+resourceID+"/messages", payload, nonce); err != nil {
		return err
	}
	return tx.Commit()
}

func ProjectConversationConfiguration(ctx context.Context, db *sql.DB, guildID, threadID string,
	conversationID uuid.UUID, inputMessageID string,
) error {
	var model, effort, tier string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(model,''), COALESCE(reasoning_effort,''), service_tier
		FROM discord_conversations WHERE id = $1`, conversationID).Scan(&model, &effort, &tier); err != nil {
		return err
	}
	card := conversationConfigurationCard(model, effort, tier)
	key := "conversation:" + conversationID.String() + ":message:" + inputMessageID
	buttons := []map[string]string{
		{"label": "按默认值开始", "customId": "codex-config-start:" + conversationID.String(), "style": "primary"},
		{"label": "调整参数", "customId": "codex-config-edit:" + conversationID.String()},
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	payload := map[string]any{"content": "", "embeds": []EmbedPayload{card}, "buttons": buttons}
	var messageID string
	err = tx.QueryRowContext(ctx, `INSERT INTO discord_projections
		(guild_id, projection_key, resource_id, desired_payload) VALUES ($1,$2,$3,$4)
		ON CONFLICT(guild_id, projection_key) DO UPDATE SET desired_payload = EXCLUDED.desired_payload,
		desired_version = discord_projections.desired_version + 1, updated_at = now()
		RETURNING COALESCE(message_id,'')`, guildID, key, threadID, mustJSON(payload)).Scan(&messageID)
	if err != nil {
		return err
	}
	operationType := "message.create"
	payload["channelId"] = threadID
	nonce := "conversation-config-" + conversationID.String()
	if messageID != "" {
		operationType, nonce = "message.update", ""
		payload["messageId"] = messageID
	}
	if err := enqueueDiscordOutbox(ctx, tx, "projection:"+key, operationType,
		"channels/"+threadID+"/messages", payload, nonce); err != nil {
		return err
	}
	return tx.Commit()
}

func ProjectConversationReply(ctx context.Context, db *sql.DB, threadID string,
	conversationID uuid.UUID, inputMessageID, content string,
) error {
	content = SanitizeDiscordResult(content)
	if content == "" {
		content = "本轮已完成。"
	}
	key := "conversation-reply:" + conversationID.String() + ":message:" + inputMessageID
	return NewSQLoutbox(db).Enqueue(ctx, key, "message.create", "channels/"+threadID+"/messages",
		map[string]any{"channelId": threadID, "content": content, "embeds": []EmbedPayload{}}, key)
}
