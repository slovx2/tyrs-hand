package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	progress := conversationProgressPayload{FormatVersion: conversationProgressFormatVersion,
		RunID: rawRunID, State: state, Summary: detail, Page: page}
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
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(model,''), COALESCE(reasoning_effort,''),
		COALESCE(service_tier,'standard')
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
	FormatVersion int                  `json:"formatVersion"`
	RunID         string               `json:"runId,omitempty"`
	State         ConversationProgress `json:"state"`
	Summary       string               `json:"summary"`
	Page          int                  `json:"page"`
}

const conversationProgressFormatVersion = 2

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

// ReconcileConversationProgressCards 分批重算旧版进度卡，原消息和 projection key 保持不变。
func ReconcileConversationProgressCards(ctx context.Context, db *sql.DB, guildID string) error {
	rows, err := db.QueryContext(ctx, `SELECT projection_key, resource_id,
		COALESCE(message_id,''), desired_payload
		FROM discord_projections
		WHERE guild_id = $1 AND projection_key LIKE 'conversation:%'
			AND desired_payload ? 'progress'
			AND COALESCE(desired_payload->'progress'->>'formatVersion','0') <> $2
		ORDER BY updated_at, projection_key LIMIT 100`, guildID,
		fmt.Sprint(conversationProgressFormatVersion))
	if err != nil {
		return err
	}
	type staleProgress struct {
		key, resourceID, messageID string
		payload                    json.RawMessage
	}
	items := make([]staleProgress, 0)
	for rows.Next() {
		var item staleProgress
		if err := rows.Scan(&item.key, &item.resourceID, &item.messageID, &item.payload); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var failures []error
	for _, item := range items {
		if err := reconcileConversationProgressCard(ctx, db, guildID, item.key,
			item.resourceID, item.messageID, item.payload); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func reconcileConversationProgressCard(ctx context.Context, db *sql.DB, guildID,
	projectionKey, resourceID, messageID string, raw json.RawMessage,
) error {
	var desired struct {
		Progress conversationProgressPayload `json:"progress"`
	}
	if err := json.Unmarshal(raw, &desired); err != nil {
		return err
	}
	runID := uuid.Nil
	if desired.Progress.RunID != "" {
		parsed, err := uuid.Parse(desired.Progress.RunID)
		if err != nil {
			return err
		}
		runID = parsed
	}
	timeline, err := conversationTimelineForRun(ctx, db, runID, desired.Progress.Summary)
	if err != nil {
		return err
	}
	desired.Progress.FormatVersion = conversationProgressFormatVersion
	desired.Progress.Page = len(timeline.Pages) - 1
	card := conversationProgressCard(desired.Progress.State, timeline, desired.Progress.Page,
		desired.Progress.RunID)
	payload := map[string]any{"channelId": resourceID, "card": card,
		"progress": desired.Progress}
	operation, nonce := "message.create", "conversation-progress-reconcile-"+projectionKey
	if messageID != "" {
		operation, nonce = "message.update", ""
		payload["messageId"] = messageID
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE discord_projections SET desired_payload = $3,
		desired_version = desired_version + 1, updated_at = now()
		WHERE guild_id = $1 AND projection_key = $2`, guildID, projectionKey,
		mustJSON(map[string]any{"card": card, "progress": desired.Progress}))
	if err == nil {
		err = enqueueDiscordOutbox(ctx, tx, "projection:"+projectionKey, operation,
			"channels/"+resourceID+"/messages", payload, nonce)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func progressButtonID(action, runID string, page int) string {
	return "codex-progress-" + action + ":" + runID + ":" + fmt.Sprint(page)
}
