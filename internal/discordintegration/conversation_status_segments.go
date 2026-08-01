package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ResolveConversationStatusBoundaryTx 用 Codex userMessage 事件确定 steer 前后的时间线边界。
func ResolveConversationStatusBoundaryTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	eventID int64, eventType string, payload json.RawMessage,
) error {
	clientID := conversationStatusBoundaryClientID(eventType, payload)
	if clientID == "" || eventID <= 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `UPDATE discord_turn_status_cards
		SET boundary_event_id=$3, updated_at=now()
		WHERE run_id=$1 AND boundary_client_id=$2 AND boundary_event_id IS NULL
		RETURNING guild_id, projection_key, revision`, runID, clientID, eventID)
	if err != nil {
		return err
	}
	type resolvedCard struct {
		guildID, key string
		revision     int64
	}
	var resolved []resolvedCard
	for rows.Next() {
		var card resolvedCard
		if err := rows.Scan(&card.guildID, &card.key, &card.revision); err != nil {
			_ = rows.Close()
			return err
		}
		resolved = append(resolved, card)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, card := range resolved {
		if err := freezePreviousConversationStatusCardTx(ctx, tx, runID, card.revision); err != nil {
			return err
		}
		if err := refreshConversationStatusCardTx(ctx, tx, runID, card.guildID, card.key); err != nil {
			return err
		}
	}
	return nil
}

func conversationStatusBoundaryClientID(eventType string, payload json.RawMessage) string {
	if eventType != "item/completed" {
		return ""
	}
	var value struct {
		Item struct {
			Type                string `json:"type"`
			ClientID            string `json:"clientId"`
			ClientUserMessageID string `json:"clientUserMessageId"`
		} `json:"item"`
	}
	if json.Unmarshal(payload, &value) != nil || value.Item.Type != "userMessage" {
		return ""
	}
	return strings.TrimSpace(firstNonEmpty(value.Item.ClientID, value.Item.ClientUserMessageID))
}

func resolveStoredConversationStatusBoundaryTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	clientID string,
) error {
	var eventID int64
	var payload json.RawMessage
	err := tx.QueryRowContext(ctx, `SELECT id, payload FROM agent_events
		WHERE run_id=$1 AND event_type='item/completed' AND payload->'item'->>'type'='userMessage'
		AND COALESCE(NULLIF(payload->'item'->>'clientId',''),
			payload->'item'->>'clientUserMessageId')=$2 ORDER BY id LIMIT 1`, runID, clientID).
		Scan(&eventID, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return ResolveConversationStatusBoundaryTx(ctx, tx, runID, eventID, "item/completed", payload)
}

func freezePreviousConversationStatusCardTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	revision int64,
) error {
	var guildID, key string
	err := tx.QueryRowContext(ctx, `SELECT guild_id, projection_key
		FROM discord_turn_status_cards WHERE run_id=$1 AND revision<$2
		ORDER BY revision DESC LIMIT 1`, runID, revision).Scan(&guildID, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return refreshConversationStatusCardTx(ctx, tx, runID, guildID, key)
}

func refreshConversationStatusCardTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	guildID, key string,
) error {
	var raw json.RawMessage
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT card.revision, projection.desired_payload
		FROM discord_turn_status_cards card JOIN discord_projections projection
		ON projection.guild_id=card.guild_id AND projection.projection_key=card.projection_key
		WHERE card.run_id=$1 AND card.projection_key=$2`, runID, key).Scan(&revision, &raw); err != nil {
		return err
	}
	var desired struct {
		Progress conversationProgressPayload `json:"progress"`
	}
	_ = json.Unmarshal(raw, &desired)
	var mode, runStatus string
	if err := tx.QueryRowContext(ctx, `SELECT collaboration_mode, status FROM codex_turn_runs
		WHERE id=$1`, runID).Scan(&mode, &runStatus); err != nil {
		return err
	}
	var hasLater bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM discord_turn_status_cards
		WHERE run_id=$1 AND revision>$2)`, runID, revision).Scan(&hasLater); err != nil {
		return err
	}
	state := conversationProgressForRunStatus(runStatus)
	if hasLater {
		state = ConversationGuided
	}
	timeline, err := conversationTimelineForStatusCard(ctx, tx, runID, key,
		desired.Progress.Summary)
	if err != nil {
		return err
	}
	desired.Progress.FormatVersion = conversationProgressFormatVersion
	desired.Progress.RunID = runID.String()
	desired.Progress.State = state
	desired.Progress.Page = len(timeline.Pages) - 1
	desired.Progress.CollaborationMode = mode
	card := conversationProgressCard(state, timeline, desired.Progress.Page, runID.String(), mode)
	return updateStatusProjectionTx(ctx, tx, guildID, key,
		map[string]any{"card": card, "progress": desired.Progress})
}

func conversationProgressForRunStatus(status string) ConversationProgress {
	switch status {
	case "completed":
		return ConversationCompleted
	case "canceled":
		return ConversationCanceled
	case "failed":
		return ConversationFailed
	default:
		return ConversationRunning
	}
}

func conversationStatusCardHasLater(ctx context.Context, db conversationQueryer,
	runID uuid.UUID, key string,
) (bool, error) {
	var hasLater bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM discord_turn_status_cards later
		JOIN discord_turn_status_cards current ON current.run_id=later.run_id
		WHERE current.run_id=$1 AND current.projection_key=$2
			AND later.revision>current.revision)`, runID, key).Scan(&hasLater)
	return hasLater, err
}

func conversationTimelineForStatusCard(ctx context.Context, db conversationQueryer,
	runID uuid.UUID, key, summary string,
) (ConversationTimeline, error) {
	var revision int64
	var boundaryClient sql.NullString
	var boundaryEvent sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT revision, boundary_client_id, boundary_event_id
		FROM discord_turn_status_cards WHERE run_id=$1 AND projection_key=$2`, runID, key).
		Scan(&revision, &boundaryClient, &boundaryEvent)
	if errors.Is(err, sql.ErrNoRows) {
		return conversationTimelineForRun(ctx, db, runID, summary)
	}
	if err != nil {
		return ConversationTimeline{}, err
	}
	if boundaryClient.Valid && !boundaryEvent.Valid {
		return ConversationTimeline{Duration: time.Second}, nil
	}
	var nextEvent sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT boundary_event_id
		FROM discord_turn_status_cards WHERE run_id=$1 AND revision>$2
		ORDER BY revision LIMIT 1`, runID, revision).Scan(&nextEvent)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ConversationTimeline{}, err
	}
	before := int64(0)
	if nextEvent.Valid {
		before = nextEvent.Int64
	}
	after := int64(0)
	if boundaryEvent.Valid {
		after = boundaryEvent.Int64
	}
	return conversationTimelineForRunRange(ctx, db, runID, summary, after, before)
}

func conversationStatusNonceForKey(key string) string {
	parts := strings.SplitN(strings.TrimPrefix(key, "conversation:"), ":message:", 2)
	if len(parts) != 2 {
		return "conversation-status-" + key
	}
	return "conversation-status-" + parts[0] + "-" + parts[1]
}
