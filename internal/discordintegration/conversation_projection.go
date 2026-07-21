package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
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
	conversationID uuid.UUID, inputMessageID string, runID uuid.UUID,
	state ConversationProgress, detail string,
) error {
	timeline, err := conversationTimelineForRun(ctx, db, runID, detail)
	if err != nil {
		return err
	}
	page := len(timeline.Pages) - 1
	rawRunID := ""
	if runID != uuid.Nil {
		rawRunID = runID.String()
	}
	card := conversationProgressCard(state, timeline, page, rawRunID)
	progress := conversationProgressPayload{RunID: rawRunID, State: state, Summary: detail,
		Page: page}
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
		mustJSON(map[string]any{"card": card, "progress": progress})).Scan(&resourceID, &messageID)
	if err != nil {
		return err
	}
	operationType := "message.create"
	payload := map[string]any{"channelId": resourceID, "card": card, "progress": progress}
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
	buttons := []ComponentButtonPayload{
		{Label: "按默认值开始", CustomID: "codex-config-start:" + conversationID.String(), Style: "primary"},
		{Label: "调整参数", CustomID: "codex-config-edit:" + conversationID.String()},
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	card.Buttons = buttons
	payload := map[string]any{"card": card}
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
		map[string]any{"channelId": threadID, "content": content}, key)
}

type conversationProgressPayload struct {
	RunID   string               `json:"runId,omitempty"`
	State   ConversationProgress `json:"state"`
	Summary string               `json:"summary"`
	Page    int                  `json:"page"`
}

func conversationTimelineForRun(ctx context.Context, db *sql.DB, runID uuid.UUID,
	summary string,
) (ConversationTimeline, error) {
	if runID == uuid.Nil {
		tracker := NewConversationActionTracker(time.Now())
		return tracker.Timeline(summary, time.Second), nil
	}
	var started time.Time
	var finished sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT started_at, finished_at FROM codex_turn_runs WHERE id = $1`,
		runID).Scan(&started, &finished); err != nil {
		return ConversationTimeline{}, err
	}
	tracker := NewConversationActionTracker(started)
	rows, err := db.QueryContext(ctx, `SELECT event_type, payload FROM agent_events
		WHERE run_id = $1 ORDER BY id`, runID)
	if err != nil {
		return ConversationTimeline{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var method string
		var params json.RawMessage
		if err := rows.Scan(&method, &params); err != nil {
			return ConversationTimeline{}, err
		}
		tracker.ApplyEvent(method, params)
	}
	if err := rows.Err(); err != nil {
		return ConversationTimeline{}, err
	}
	end := time.Now()
	if finished.Valid {
		end = finished.Time
	}
	return tracker.Timeline(summary, end.Sub(started)), nil
}

func progressButtonID(action, runID string, page int) string {
	return "codex-progress-" + action + ":" + runID + ":" + fmt.Sprint(page)
}
