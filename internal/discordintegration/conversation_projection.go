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
	"unicode"
	"unicode/utf8"

	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
)

var discordSecretPattern = regexp.MustCompile(`(?i)\b(?:sk|ghp|github_pat)_[a-z0-9_-]{12,}\b|\bBearer\s+[a-z0-9._~+/-]{12,}`)

const (
	discordReplyMessageBudget = 1900
	replyPreviousLabel        = "【上接消息】"
	replyNextLabel            = "【内容未完整，下接消息】"
	replyFenceReserve         = 128
)

func SanitizeDiscordResult(value string) string {
	value = strings.TrimSpace(value)
	return discordSecretPattern.ReplaceAllString(value, "[已隐藏凭据]")
}

func ProjectConversationStatus(ctx context.Context, db *sql.DB, guildID, threadID string,
	conversationID uuid.UUID, inputMessageID string, runID uuid.UUID,
	state ConversationProgress, detail string,
) error {
	rawRunID := ""
	mode := "default"
	var err error
	if runID != uuid.Nil {
		rawRunID = runID.String()
		err = db.QueryRowContext(ctx, `SELECT collaboration_mode FROM codex_turn_runs
			WHERE id = $1`, runID).Scan(&mode)
	} else {
		err = db.QueryRowContext(ctx, `SELECT collaboration_mode FROM discord_conversations
			WHERE id = $1`, conversationID).Scan(&mode)
	}
	if err != nil {
		return err
	}
	requestedKey := "conversation:" + conversationID.String() + ":message:" + inputMessageID
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	key := requestedKey
	if runID != uuid.Nil {
		key, err = currentConversationStatusKeyTx(ctx, tx, runID, requestedKey)
		if err != nil {
			return err
		}
	}
	var timeline ConversationTimeline
	if runID == uuid.Nil {
		timeline, err = conversationTimelineForRun(ctx, tx, runID, detail)
	} else {
		timeline, err = conversationTimelineForStatusCard(ctx, tx, runID, key, detail)
	}
	if err != nil {
		return err
	}
	page := len(timeline.Pages) - 1
	card := conversationProgressCard(state, timeline, page, rawRunID, mode)
	progress := conversationProgressPayload{FormatVersion: conversationProgressFormatVersion,
		RunID: rawRunID, State: state, Summary: detail, Page: page, CollaborationMode: mode}
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
	if runID != uuid.Nil {
		if err := registerInitialConversationStatusTx(ctx, tx, runID, guildID, key); err != nil {
			return err
		}
	}
	operationType := "message.create"
	payload := map[string]any{"channelId": resourceID, "card": card, "progress": progress}
	nonce := conversationStatusNonceForKey(key)
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

func ProjectConversationThinkingTx(ctx context.Context, tx *sql.Tx, guildID, threadID string,
	conversationID uuid.UUID, inputMessageID string,
) error {
	var mode string
	if err := tx.QueryRowContext(ctx, `SELECT collaboration_mode FROM discord_conversations
		WHERE id = $1`, conversationID).Scan(&mode); err != nil {
		return err
	}
	timeline := ConversationTimeline{Duration: time.Second}
	card := conversationProgressCard(ConversationRunning, timeline, 0, "", mode)
	progress := conversationProgressPayload{FormatVersion: conversationProgressFormatVersion,
		State: ConversationRunning, CollaborationMode: mode}
	key := "conversation:" + conversationID.String() + ":message:" + inputMessageID
	var resourceID, messageID string
	err := tx.QueryRowContext(ctx, `INSERT INTO discord_projections
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
	operation, nonce := "message.create", conversationStatusNonceForKey(key)
	payload := map[string]any{"channelId": resourceID, "card": card, "progress": progress}
	if messageID != "" {
		operation, nonce = "message.update", ""
		payload["messageId"] = messageID
	}
	return enqueueDiscordOutbox(ctx, tx, "projection:"+key, operation,
		"channels/"+resourceID+"/messages", payload, nonce)
}

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
	if err := upsertWaitingConfigurationProjectionTx(ctx, tx, guildID, threadID,
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
	operationType := "message.create"
	payload["channelId"] = threadID
	nonce := "conversation-config-" + state.ConversationID.String()
	if messageID != "" {
		operationType, nonce = "message.update", ""
		payload["messageId"] = messageID
	}
	if err := enqueueDiscordOutbox(ctx, tx, "projection:"+key, operationType,
		"channels/"+threadID+"/messages", payload, nonce); err != nil {
		return err
	}
	return nil
}

func ProjectConversationReply(ctx context.Context, db *sql.DB, threadID string,
	conversationID uuid.UUID, inputMessageID string, runID uuid.UUID, content, finalOutputType string,
	replyMessageID ...string,
) error {
	content = SanitizeDiscordResult(content)
	if content == "" {
		content = "本轮已完成。"
	}
	mentionSource := inputMessageID
	if len(replyMessageID) > 0 && replyMessageID[0] != "" {
		mentionSource = replyMessageID[0]
	}
	mentionUserID, err := conversationReplyMentionUser(ctx, db, conversationID, mentionSource)
	if err != nil {
		return err
	}
	key := "conversation-reply:" + conversationID.String() + ":message:" + inputMessageID
	payload := conversationReplyPayload(threadID, content, mentionUserID)
	guildID, mode, err := conversationReplyMode(ctx, db, conversationID, threadID, runID)
	if err != nil {
		return err
	}
	actionablePlan := mode == "plan" && finalOutputType == "plan"
	if mode != "plan" &&
		utf8.RuneCountInString(textValue(payload["content"])) <= discordReplyMessageBudget {
		return projectConversationReplyPages(ctx, db, guildID, threadID, key, mentionUserID,
			[]string{content}, actionablePlan, runID)
	}
	chunks := splitConversationReply(content, guildID, threadID, mentionUserID)
	if len(chunks) == 0 || (mode != "plan" && len(chunks) < 2) {
		return errors.New("discord 长回复分片失败")
	}
	return projectConversationReplyPages(ctx, db, guildID, threadID, key, mentionUserID,
		chunks, actionablePlan, runID)
}

// ProjectConversationReplyRegenerating 原位失效旧结果和 Plan 按钮。
func ProjectConversationReplyRegenerating(ctx context.Context, db *sql.DB, threadID string,
	conversationID uuid.UUID, inputMessageID string,
) error {
	var guildID string
	if err := db.QueryRowContext(ctx, `SELECT guild_id FROM discord_conversations
		WHERE id=$1 AND thread_id=$2`, conversationID, threadID).Scan(&guildID); err != nil {
		return err
	}
	key := "conversation-reply:" + conversationID.String() + ":message:" + inputMessageID
	return projectConversationReplyPages(ctx, db, guildID, threadID, key, "",
		[]string{"消息已编辑，正在重新生成。"}, false, uuid.Nil)
}

func projectConversationReplyPages(ctx context.Context, db *sql.DB, guildID, threadID,
	baseKey, mentionUserID string, chunks []string, actionablePlan bool, runID uuid.UUID,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	total := len(chunks)
	if actionablePlan {
		total++
	}
	for page := 0; page < total; page++ {
		key := baseKey
		if page > 0 {
			key += ":page:" + fmt.Sprint(page)
		}
		payload := map[string]any{"channelId": threadID}
		if page < len(chunks) {
			content := chunks[page]
			if len(chunks) > 1 {
				content = fmt.Sprintf("**Codex 回复 · %d/%d**\n%s", page+1, len(chunks), content)
			}
			payload = conversationReplyPayload(threadID, content, "")
			if page == 0 && mentionUserID != "" {
				payload = conversationReplyPayload(threadID, content, mentionUserID)
			}
		} else {
			payload["card"] = planCompletedCard(runID)
		}
		var messageID string
		if err := tx.QueryRowContext(ctx, `INSERT INTO discord_projections
			(guild_id,projection_key,resource_id,desired_payload) VALUES ($1,$2,$3,$4)
			ON CONFLICT(guild_id,projection_key) DO UPDATE SET
			resource_id=EXCLUDED.resource_id,desired_payload=EXCLUDED.desired_payload,
			desired_version=discord_projections.desired_version+1,updated_at=now()
			RETURNING COALESCE(message_id,'')`, guildID, key, threadID,
			mustJSON(payload)).Scan(&messageID); err != nil {
			return err
		}
		operation, nonce := "message.create", key
		if messageID != "" {
			operation, nonce = "message.update", ""
			payload["messageId"] = messageID
		}
		if err := enqueueDiscordOutbox(ctx, tx, "projection:"+key, operation,
			"channels/"+threadID+"/messages", payload, nonce); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT projection_key,COALESCE(message_id,'')
		FROM discord_projections WHERE guild_id=$1 AND
		(projection_key=$2 OR left(projection_key,length($2)+6)=$2 || ':page:')`,
		guildID, baseKey)
	if err != nil {
		return err
	}
	type obsoletePage struct {
		key       string
		messageID string
	}
	obsolete := make([]obsoletePage, 0)
	for rows.Next() {
		var key, messageID string
		if err := rows.Scan(&key, &messageID); err != nil {
			_ = rows.Close()
			return err
		}
		page := 0
		if key != baseKey {
			if _, scanErr := fmt.Sscanf(strings.TrimPrefix(key, baseKey), ":page:%d", &page); scanErr != nil {
				continue
			}
		}
		if page < total {
			continue
		}
		obsolete = append(obsolete, obsoletePage{key: key, messageID: messageID})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, page := range obsolete {
		if page.messageID != "" {
			if err := enqueueDiscordOutbox(ctx, tx, "projection-delete:"+page.key,
				"message.delete", "channels/"+threadID+"/messages/"+page.messageID,
				map[string]any{"channelId": threadID, "messageId": page.messageID}, ""); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM discord_projections
			WHERE guild_id=$1 AND projection_key=$2`, guildID, page.key); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func conversationReplyMode(ctx context.Context, db *sql.DB, conversationID uuid.UUID,
	threadID string, runID uuid.UUID,
) (string, string, error) {
	var guildID, mode string
	if runID == uuid.Nil {
		err := db.QueryRowContext(ctx, `SELECT guild_id, collaboration_mode
			FROM discord_conversations WHERE id = $1 AND thread_id = $2`,
			conversationID, threadID).Scan(&guildID, &mode)
		return guildID, mode, err
	}
	err := db.QueryRowContext(ctx, `SELECT conversation.guild_id, run.collaboration_mode
		FROM codex_turn_runs run
		JOIN codex_thread_controls control ON control.id = run.control_id
		JOIN discord_conversations conversation
			ON conversation.id = control.discord_conversation_id
		WHERE run.id = $1 AND conversation.id = $2 AND conversation.thread_id = $3`,
		runID, conversationID, threadID).Scan(&guildID, &mode)
	return guildID, mode, err
}

func planCompletedCard(runID uuid.UUID) ComponentCardPayload {
	return ComponentCardPayload{AccentColor: cardColorGreen, Header: "📋 Codex · Plan 已完成",
		Buttons: []ComponentButtonPayload{{Label: "执行计划",
			CustomID: "codex-plan-execute:" + runID.String(), Style: "primary"}}}
}

func replyJumpURL(guildID, threadID, messageID string) string {
	return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, threadID, messageID)
}

func replyPreviousLink(guildID, threadID, messageID string) string {
	return fmt.Sprintf("[%s](%s)\n\n", replyPreviousLabel,
		replyJumpURL(guildID, threadID, messageID))
}

func replyNextLink(guildID, threadID, messageID string) string {
	return fmt.Sprintf("\n\n[%s](%s)", replyNextLabel,
		replyJumpURL(guildID, threadID, messageID))
}

func splitConversationReply(content, guildID, threadID, mentionUserID string) []string {
	placeholderID := strings.Repeat("9", 20)
	prefix := replyPreviousLink(guildID, threadID, placeholderID)
	suffix := replyNextLink(guildID, threadID, placeholderID)
	mention := ""
	if mentionUserID != "" {
		mention = "<@" + mentionUserID + "> "
	}
	wrapper := max(utf8.RuneCountInString(prefix+suffix),
		utf8.RuneCountInString(mention+suffix))
	budget := discordReplyMessageBudget - wrapper - replyFenceReserve
	if budget < 256 {
		budget = 256
	}
	return balanceReplyCodeFences(splitReplyText(content, budget))
}

func splitReplyText(content string, budget int) []string {
	remaining := []rune(strings.TrimSpace(content))
	var chunks []string
	for len(remaining) > budget {
		cut, boundary := replyChunkBoundary(remaining, budget)
		part, rest := remaining[:cut], remaining[cut:]
		switch boundary {
		case 'n':
			part = trimTrailingNewlines(part)
			rest = trimLeadingNewlines(rest)
		case 's':
			part = trimTrailingSpaces(part)
			rest = trimLeadingSpaces(rest)
		}
		if len(part) == 0 {
			part, rest = remaining[:budget], remaining[budget:]
		}
		chunks = append(chunks, string(part))
		remaining = rest
	}
	if len(remaining) > 0 {
		chunks = append(chunks, string(remaining))
	}
	return chunks
}

func replyChunkBoundary(value []rune, budget int) (int, rune) {
	limit := min(len(value), budget)
	for index := limit - 1; index >= 0; index-- {
		if value[index] == '\n' {
			return index + 1, 'n'
		}
	}
	for index := limit - 1; index >= 0; index-- {
		if unicode.IsSpace(value[index]) || strings.ContainsRune("，。；！？、,.!?;:", value[index]) {
			boundary := 'p'
			if unicode.IsSpace(value[index]) {
				boundary = 's'
			}
			return index + 1, boundary
		}
	}
	return limit, 'h'
}

func trimTrailingNewlines(value []rune) []rune {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}

func trimLeadingNewlines(value []rune) []rune {
	for len(value) > 0 && (value[0] == '\n' || value[0] == '\r') {
		value = value[1:]
	}
	return value
}

func trimTrailingSpaces(value []rune) []rune {
	for len(value) > 0 && unicode.IsSpace(value[len(value)-1]) {
		value = value[:len(value)-1]
	}
	return value
}

func trimLeadingSpaces(value []rune) []rune {
	for len(value) > 0 && unicode.IsSpace(value[0]) {
		value = value[1:]
	}
	return value
}

type replyFenceState struct {
	marker  string
	opening string
}

func balanceReplyCodeFences(chunks []string) []string {
	state := replyFenceState{}
	result := make([]string, 0, len(chunks))
	for index, chunk := range chunks {
		prefix := ""
		if state.marker != "" {
			prefix = state.opening + "\n"
		}
		state = scanReplyCodeFences(chunk, state)
		body := prefix + chunk
		if state.marker != "" && index < len(chunks)-1 {
			body += "\n" + state.marker
		}
		result = append(result, body)
	}
	return result
}

func scanReplyCodeFences(value string, state replyFenceState) replyFenceState {
	for line := range strings.SplitSeq(value, "\n") {
		trimmed := strings.TrimSpace(line)
		marker := replyFenceMarker(trimmed)
		if marker == "" {
			continue
		}
		if state.marker == "" {
			if utf8.RuneCountInString(trimmed) <= 96 {
				state = replyFenceState{marker: marker, opening: trimmed}
			}
			continue
		}
		if marker[0] == state.marker[0] && strings.TrimSpace(strings.TrimPrefix(trimmed, marker)) == "" {
			state = replyFenceState{}
		}
	}
	return state
}

func replyFenceMarker(value string) string {
	if len(value) < 3 || (value[0] != '`' && value[0] != '~') {
		return ""
	}
	index := 1
	for index < len(value) && value[index] == value[0] {
		index++
	}
	if index < 3 {
		return ""
	}
	return value[:index]
}

func conversationReplyMentionUser(ctx context.Context, db *sql.DB, conversationID uuid.UUID,
	inputMessageID string,
) (string, error) {
	var userID string
	var multiplayer bool
	err := db.QueryRowContext(ctx, `SELECT message.discord_user_id,
		EXISTS(SELECT 1
		 FROM discord_input_messages participant
		 WHERE participant.conversation_id = message.conversation_id
		   AND participant.discord_user_id <> message.discord_user_id)
		FROM discord_input_messages message
		WHERE message.conversation_id = $1 AND message.message_id = $2`,
		conversationID, inputMessageID).Scan(&userID, &multiplayer)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !multiplayer {
		return "", nil
	}
	if _, err := snowflake.Parse(userID); err != nil {
		return "", nil
	}
	return userID, nil
}

func conversationReplyPayload(threadID, content, mentionUserID string) map[string]any {
	payload := map[string]any{"channelId": threadID, "content": content}
	if mentionUserID != "" {
		payload["content"] = "<@" + mentionUserID + "> " + content
		payload["mentionUserIds"] = []string{mentionUserID}
	}
	return payload
}

type conversationProgressPayload struct {
	FormatVersion     int                  `json:"formatVersion"`
	RunID             string               `json:"runId,omitempty"`
	State             ConversationProgress `json:"state"`
	Summary           string               `json:"summary"`
	Page              int                  `json:"page"`
	CollaborationMode string               `json:"collaborationMode"`
}

const conversationProgressFormatVersion = 5

type conversationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func conversationTimelineForRun(ctx context.Context, db conversationQueryer, runID uuid.UUID,
	summary string,
) (ConversationTimeline, error) {
	return conversationTimelineForRunRange(ctx, db, runID, summary, 0, 0)
}

func conversationTimelineForRunRange(ctx context.Context, db conversationQueryer, runID uuid.UUID,
	summary string, afterEventID, beforeEventID int64,
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
	if afterEventID > 0 {
		if err := db.QueryRowContext(ctx, `SELECT occurred_at FROM agent_events
			WHERE run_id=$1 AND id=$2`, runID, afterEventID).Scan(&started); err != nil {
			return ConversationTimeline{}, err
		}
	}
	tracker := NewConversationActionTracker(started)
	rows, err := db.QueryContext(ctx, `SELECT event_type, payload FROM agent_events
		WHERE run_id=$1 AND id>$2 AND ($3=0 OR id<$3) ORDER BY id`,
		runID, afterEventID, beforeEventID)
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
	if beforeEventID > 0 {
		if err := db.QueryRowContext(ctx, `SELECT occurred_at FROM agent_events
			WHERE run_id=$1 AND id=$2`, runID, beforeEventID).Scan(&end); err != nil {
			return ConversationTimeline{}, err
		}
	} else if finished.Valid {
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
			AND (
				COALESCE(desired_payload->'progress'->>'formatVersion','0') <> $2
				OR EXISTS (
					SELECT 1 FROM codex_turn_runs AS run
					WHERE run.id::text = desired_payload->'progress'->>'runId'
						AND NOT EXISTS (
							SELECT 1
							FROM discord_turn_status_cards AS current_card
							JOIN discord_turn_status_cards AS later_card
								ON later_card.run_id = current_card.run_id
								AND later_card.revision > current_card.revision
							WHERE current_card.run_id = run.id
								AND current_card.projection_key = discord_projections.projection_key
						)
						AND (
							(run.status = 'canceled'
								AND COALESCE(desired_payload->'progress'->>'state','') <> 'canceled')
							OR
							(run.status = 'failed'
								AND COALESCE(desired_payload->'progress'->>'state','') <> 'failed')
						)
				)
			)
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
		var runStatus, mode string
		err = db.QueryRowContext(ctx, `SELECT status, collaboration_mode FROM codex_turn_runs WHERE id = $1`,
			runID).Scan(&runStatus, &mode)
		if errors.Is(err, sql.ErrNoRows) {
			runID = uuid.Nil
		} else if err != nil {
			return err
		}
		if runID != uuid.Nil {
			desired.Progress.CollaborationMode = mode
		}
		switch runStatus {
		case "canceled":
			desired.Progress.State = ConversationCanceled
			desired.Progress.Summary = "本轮已停止。"
		case "failed":
			desired.Progress.State = ConversationFailed
			desired.Progress.Summary = "本轮处理未完成。"
		}
	}
	if desired.Progress.CollaborationMode == "" {
		parts := strings.Split(projectionKey, ":")
		if len(parts) >= 2 {
			conversationID, parseErr := uuid.Parse(parts[1])
			if parseErr == nil {
				_ = db.QueryRowContext(ctx, `SELECT collaboration_mode
					FROM discord_conversations WHERE id = $1`, conversationID).
					Scan(&desired.Progress.CollaborationMode)
			}
		}
	}
	if runID != uuid.Nil {
		hasLater, laterErr := conversationStatusCardHasLater(ctx, db, runID, projectionKey)
		if laterErr != nil {
			return laterErr
		}
		if hasLater {
			desired.Progress.State = ConversationGuided
		}
	}
	var timeline ConversationTimeline
	var err error
	if runID == uuid.Nil {
		timeline, err = conversationTimelineForRun(ctx, db, runID, desired.Progress.Summary)
	} else {
		timeline, err = conversationTimelineForStatusCard(ctx, db, runID, projectionKey,
			desired.Progress.Summary)
	}
	if err != nil {
		return err
	}
	desired.Progress.FormatVersion = conversationProgressFormatVersion
	desired.Progress.Page = len(timeline.Pages) - 1
	card := conversationProgressCard(desired.Progress.State, timeline, desired.Progress.Page,
		desired.Progress.RunID, desired.Progress.CollaborationMode)
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
