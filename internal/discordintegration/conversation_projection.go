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

func ProjectConversationStatus(ctx context.Context, db *sql.DB, guildID, threadID string, conversationID uuid.UUID, content string) error {
	content = SanitizeDiscordResult(content)
	key := "conversation:" + conversationID.String()
	var resourceID, messageID string
	err := db.QueryRowContext(ctx, `INSERT INTO discord_projections
		(guild_id, projection_key, resource_id, desired_payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT(guild_id, projection_key) DO UPDATE SET
			resource_id = EXCLUDED.resource_id, desired_payload = EXCLUDED.desired_payload,
			desired_version = discord_projections.desired_version + 1, updated_at = now()
		RETURNING resource_id, COALESCE(message_id, '')`, guildID, key, threadID,
		mustJSON(map[string]string{"content": content})).Scan(&resourceID, &messageID)
	if err != nil {
		return err
	}
	operationType := "message.create"
	payload := map[string]string{"channelId": resourceID, "content": content}
	nonce := "conversation-status-" + conversationID.String()
	if messageID != "" {
		operationType = "message.update"
		payload["messageId"] = messageID
		nonce = ""
	}
	return NewSQLoutbox(db).Enqueue(ctx, "projection:"+key, operationType,
		"channels/"+resourceID+"/messages", payload, nonce)
}
