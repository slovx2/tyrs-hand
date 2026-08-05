package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type runEventProjection struct {
	Sequence   int64
	Type       string
	Payload    json.RawMessage
	OccurredAt time.Time
}

func ensureInitialRunSegmentTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID) (uuid.UUID, error) {
	var segmentID uuid.UUID
	err := tx.QueryRowContext(ctx, `INSERT INTO run_process_segments(
		run_id,sequence,trigger_type,trigger_message_id,start_event_sequence)
		SELECT run.id,0,'initial',message.id,0
		FROM codex_turn_runs run
		LEFT JOIN LATERAL (
			SELECT id FROM session_messages
			WHERE conversation_turn_id=run.primary_intent_id AND message_role='user'
			ORDER BY seq LIMIT 1
		) message ON true
		WHERE run.id=$1
		ON CONFLICT(run_id,sequence) DO UPDATE SET updated_at=now()
		RETURNING id`, runID).Scan(&segmentID)
	return segmentID, err
}

func runEventItem(raw json.RawMessage) (map[string]any, map[string]any) {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return map[string]any{}, map[string]any{}
	}
	item, _ := payload["item"].(map[string]any)
	if item == nil {
		item = map[string]any{}
	}
	return payload, item
}

func mapString(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return strings.TrimSpace(result)
}

func ensureUserBoundaryTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	event runEventProjection, payload, item map[string]any,
) error {
	if event.Type != "item/completed" || mapString(item, "type") != "userMessage" {
		return nil
	}
	clientID := mapString(item, "clientId")
	if clientID == "" {
		clientID = mapString(item, "clientUserMessageId")
	}
	if clientID == "" {
		clientID = mapString(item, "id")
	}
	if clientID == "" {
		clientID = fmt.Sprintf("event-%d", event.Sequence)
	}
	triggerMessageID, err := runBoundaryMessageIDTx(ctx, tx, runID, event.Sequence, clientID)
	if err != nil {
		return err
	}
	initialID, err := ensureInitialRunSegmentTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	var initialBoundary sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT boundary_client_id FROM run_process_segments
		WHERE id=$1 FOR UPDATE`, initialID).Scan(&initialBoundary); err != nil {
		return err
	}
	if !initialBoundary.Valid {
		_, err = tx.ExecContext(ctx, `UPDATE run_process_segments SET boundary_client_id=$2,
			trigger_message_id=COALESCE(trigger_message_id,$3),updated_at=now() WHERE id=$1`,
			initialID, clientID, triggerMessageID)
		return err
	}
	var existing uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT id FROM run_process_segments
		WHERE run_id=$1 AND boundary_client_id=$2`, runID, clientID).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var next int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(sequence),0)+1
		FROM run_process_segments WHERE run_id=$1`, runID).Scan(&next); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE run_process_segments
		SET end_event_sequence=$2,updated_at=now()
		WHERE run_id=$1 AND sequence=$3 AND end_event_sequence IS NULL`, runID,
		event.Sequence, next-1); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_process_segments(
		run_id,sequence,trigger_type,trigger_message_id,boundary_client_id,start_event_sequence)
		VALUES ($1,$2,'steer',$3,$4,$5)`, runID, next, triggerMessageID, clientID,
		event.Sequence)
	_ = payload
	return err
}

func runBoundaryMessageIDTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	eventSequence int64, boundaryID string,
) (*uuid.UUID, error) {
	var messageID uuid.UUID
	err := tx.QueryRowContext(ctx, `SELECT message.id FROM session_messages message
		LEFT JOIN codex_turn_intents intent ON intent.id=message.turn_intent_id
		JOIN codex_turn_runs run ON run.primary_intent_id=message.conversation_turn_id
		WHERE run.id=$1 AND message.message_role='user' AND (
			message.local_id=$2 OR message.turn_intent_id::text=$2
			OR intent.desktop_input_projection_key=$2
			OR intent.codex_user_message_item_id=$2)
		ORDER BY message.seq LIMIT 1`, runID, boundaryID).Scan(&messageID)
	if err == nil {
		return &messageID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT message.id
		FROM codex_turn_runs run
		JOIN LATERAL (
			SELECT candidate.id FROM session_messages candidate
			WHERE candidate.conversation_turn_id=run.primary_intent_id
				AND candidate.message_role='user'
			ORDER BY candidate.seq
			OFFSET (SELECT GREATEST(count(*)-1,0) FROM agent_events event
				WHERE event.run_id=run.id AND event.run_event_sequence<=$2
					AND event.event_type='item/completed'
					AND event.payload->'item'->>'type'='userMessage')
			LIMIT 1
		) message ON true
		WHERE run.id=$1`, runID, eventSequence).Scan(&messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &messageID, nil
}

func segmentForRunEventTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	sequence int64,
) (uuid.UUID, error) {
	if _, err := ensureInitialRunSegmentTx(ctx, tx, runID); err != nil {
		return uuid.Nil, err
	}
	var segmentID uuid.UUID
	err := tx.QueryRowContext(ctx, `SELECT id FROM run_process_segments
		WHERE run_id=$1 AND start_event_sequence<=$2
		AND (end_event_sequence IS NULL OR end_event_sequence>$2)
		ORDER BY sequence DESC LIMIT 1`, runID, sequence).Scan(&segmentID)
	return segmentID, err
}

func projectRunEventTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	event runEventProjection,
) error {
	payload, item := runEventItem(event.Payload)
	if err := ensureUserBoundaryTx(ctx, tx, runID, event, payload, item); err != nil {
		return err
	}
	itemType := mapString(item, "type")
	if itemType == "userMessage" || event.Type == "runtime.settings_applied" {
		return nil
	}
	segmentID, err := segmentForRunEventTx(ctx, tx, runID, event.Sequence)
	if err != nil {
		return err
	}
	phase := mapString(item, "phase")
	if phase == "" {
		phase = mapString(payload, "phase")
	}
	itemID := mapString(item, "id")
	if itemID == "" {
		itemID = mapString(payload, "itemId")
	}
	if itemID == "" {
		itemID = fmt.Sprintf("event-%d", event.Sequence)
	}
	var existingSegment uuid.UUID
	if queryErr := tx.QueryRowContext(ctx, `SELECT segment_id FROM run_process_activities
		WHERE run_id=$1 AND item_id=$2 ORDER BY first_event_sequence LIMIT 1`, runID,
		itemID).Scan(&existingSegment); queryErr == nil {
		segmentID = existingSegment
	} else if !errors.Is(queryErr, sql.ErrNoRows) {
		return queryErr
	}
	if itemType == "agentMessage" {
		if phase != "commentary" && phase != "final_answer" {
			return nil
		}
		kind := phase
		text := mapString(item, "text")
		return upsertRunTextActivity(ctx, tx, runID, segmentID, itemID, kind,
			event, text, true)
	}
	if event.Type == "item/agentMessage/delta" || event.Type == "item/delta" {
		kind := phase
		if kind == "" {
			_ = tx.QueryRowContext(ctx, `SELECT kind FROM run_process_activities
				WHERE segment_id=$1 AND item_id=$2`, segmentID, itemID).Scan(&kind)
		}
		if kind != "commentary" && kind != "final_answer" {
			return nil
		}
		text := mapString(payload, "delta")
		if text == "" {
			text = mapString(payload, "text")
		}
		return upsertRunTextActivity(ctx, tx, runID, segmentID, itemID, kind,
			event, text, false)
	}
	if event.Type != "item/started" && event.Type != "item/completed" &&
		event.Type != "discord/tool/started" && event.Type != "discord/tool/completed" {
		return nil
	}
	if itemType == "" {
		return nil
	}
	status := "running"
	if strings.HasSuffix(event.Type, "completed") {
		status = "completed"
	}
	if mapString(item, "status") == "failed" {
		status = "failed"
	}
	encoded, _ := json.Marshal(map[string]any{"item": item, "eventType": event.Type})
	_, err = tx.ExecContext(ctx, `INSERT INTO run_process_activities(
		run_id,segment_id,item_id,kind,first_event_sequence,last_event_sequence,status,payload,occurred_at)
		VALUES ($1,$2,$3,'operation',$4,$4,$5,$6,$7)
		ON CONFLICT(segment_id,item_id) DO UPDATE SET
		last_event_sequence=EXCLUDED.last_event_sequence,status=EXCLUDED.status,
		payload=EXCLUDED.payload,updated_at=now()
		WHERE EXCLUDED.last_event_sequence>run_process_activities.last_event_sequence`,
		runID, segmentID, itemID, event.Sequence, status, encoded, event.OccurredAt)
	return err
}

func upsertRunTextActivity(ctx context.Context, tx *sql.Tx, runID, segmentID uuid.UUID,
	itemID, kind string, event runEventProjection, value string, replace bool,
) error {
	encoded, _ := json.Marshal(map[string]any{"text": value})
	if replace {
		_, err := tx.ExecContext(ctx, `INSERT INTO run_process_activities(
			run_id,segment_id,item_id,kind,first_event_sequence,last_event_sequence,status,payload,occurred_at)
			VALUES ($1,$2,$3,$4,$5,$5,'completed',$6,$7)
			ON CONFLICT(segment_id,item_id) DO UPDATE SET
			last_event_sequence=EXCLUDED.last_event_sequence,status='completed',payload=EXCLUDED.payload,
			updated_at=now() WHERE EXCLUDED.last_event_sequence>=run_process_activities.last_event_sequence`,
			runID, segmentID, itemID, kind, event.Sequence, encoded, event.OccurredAt)
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO run_process_activities(
		run_id,segment_id,item_id,kind,first_event_sequence,last_event_sequence,status,payload,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$5,'running',$6,$7)
		ON CONFLICT(segment_id,item_id) DO UPDATE SET
		last_event_sequence=EXCLUDED.last_event_sequence,
		payload=jsonb_build_object('text',COALESCE(run_process_activities.payload->>'text','') ||
			COALESCE(EXCLUDED.payload->>'text','')),updated_at=now()
		WHERE EXCLUDED.last_event_sequence>run_process_activities.last_event_sequence`,
		runID, segmentID, itemID, kind, event.Sequence, encoded, event.OccurredAt)
	return err
}

func (s *Server) ensureRunProjection(ctx context.Context, runID uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = ensureInitialRunSegmentTx(ctx, tx, runID); err != nil {
		return err
	}
	var watermark int64
	if err = tx.QueryRowContext(ctx, `SELECT client_projection_sequence
		FROM codex_turn_runs WHERE id=$1 FOR UPDATE`, runID).Scan(&watermark); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT run_event_sequence,event_type,payload,occurred_at
		FROM agent_events WHERE run_id=$1 AND run_event_sequence>$2
		ORDER BY run_event_sequence`, runID, watermark)
	if err != nil {
		return err
	}
	events := make([]runEventProjection, 0)
	for rows.Next() {
		var event runEventProjection
		if err = rows.Scan(&event.Sequence, &event.Type, &event.Payload,
			&event.OccurredAt); err != nil {
			_ = rows.Close()
			return err
		}
		events = append(events, event)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, event := range events {
		if err = projectRunEventTx(ctx, tx, runID, event); err != nil {
			return err
		}
	}
	if len(events) > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET client_projection_sequence=$2
			WHERE id=$1`, runID, events[len(events)-1].Sequence)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func createInteractiveSegmentTx(ctx context.Context, tx *sql.Tx, requestID uuid.UUID) error {
	var runID, turnID uuid.UUID
	var sessionID sql.NullString
	var watermark int64
	if err := tx.QueryRowContext(ctx, `SELECT run.id,run.worker_event_sequence,
		run.primary_intent_id,intent.session_id::text
		FROM codex_interactive_requests request
		JOIN codex_turn_runs run ON run.id=request.run_id
		JOIN codex_turn_intents intent ON intent.id=run.primary_intent_id
		WHERE request.id=$1 FOR UPDATE OF run`, requestID).
		Scan(&runID, &watermark, &turnID, &sessionID); err != nil {
		return err
	}
	if _, err := ensureInitialRunSegmentTx(ctx, tx, runID); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_process_segments
		WHERE interactive_request_id=$1)`, requestID).Scan(&exists); err != nil || exists {
		return err
	}
	var next int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(sequence),0)+1
		FROM run_process_segments WHERE run_id=$1`, runID).Scan(&next); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_process_segments
		SET end_event_sequence=$2,updated_at=now()
		WHERE run_id=$1 AND sequence=$3 AND end_event_sequence IS NULL`, runID,
		watermark+1, next-1); err != nil {
		return err
	}
	var segmentID uuid.UUID
	err := tx.QueryRowContext(ctx, `INSERT INTO run_process_segments(
		run_id,sequence,trigger_type,interactive_request_id,start_event_sequence)
		VALUES ($1,$2,'interactive',$3,$4) RETURNING id`, runID, next, requestID,
		watermark+1).Scan(&segmentID)
	if err != nil || !sessionID.Valid {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"conversationTurnId": turnID,
		"runId": runID, "segmentId": segmentID})
	_, err = tx.ExecContext(ctx, `INSERT INTO client_updates(
		session_id,update_type,entity_type,entity_id,payload)
		VALUES ($1,'run.segment.updated','turn',$2,$3)`, sessionID.String,
		turnID.String(), payload)
	return err
}
