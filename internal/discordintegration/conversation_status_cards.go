package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

func currentConversationStatusKeyTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	fallback string,
) (string, error) {
	if _, err := ensureInitialConversationStatusTx(ctx, tx, runID, fallback); err != nil {
		return "", err
	}
	var key string
	err := tx.QueryRowContext(ctx, `SELECT projection_key FROM discord_turn_status_cards
		WHERE run_id = $1 ORDER BY revision DESC LIMIT 1`, runID).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	return key, err
}

func registerInitialConversationStatusTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	guildID, projectionKey string,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO discord_turn_status_cards
		(run_id, guild_id, projection_key, revision, role)
		VALUES ($1,$2,$3,0,'current') ON CONFLICT DO NOTHING`, runID, guildID, projectionKey)
	return err
}

func ensureInitialConversationStatusTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	fallback string,
) (string, error) {
	if err := tx.QueryRowContext(ctx, `SELECT id FROM codex_turn_runs WHERE id = $1 FOR UPDATE`,
		runID).Scan(&runID); err != nil {
		return "", err
	}
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT projection_key FROM discord_turn_status_cards
		WHERE run_id = $1 AND role = 'current'`, runID).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var guildID, projectionKey string
	if fallback != "" {
		projectionKey = fallback
		err = tx.QueryRowContext(ctx, `SELECT guild_id FROM discord_projections
			WHERE projection_key = $1`, projectionKey).Scan(&guildID)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT conversation.guild_id,
			'conversation:' || conversation.id::text || ':message:' ||
				COALESCE(intent.discord_message_id, 'desktop-' || intent.id::text)
			FROM codex_turn_runs run
			JOIN codex_turn_intents intent ON intent.id = run.primary_intent_id
			JOIN discord_conversations conversation ON conversation.id = intent.discord_conversation_id
			WHERE run.id = $1`, runID).
			Scan(&guildID, &projectionKey)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var projectionExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM discord_projections
		WHERE guild_id=$1 AND projection_key=$2)`, guildID, projectionKey).
		Scan(&projectionExists); err != nil {
		return "", err
	}
	if !projectionExists {
		return "", nil
	}
	if err := registerInitialConversationStatusTx(ctx, tx, runID, guildID, projectionKey); err != nil {
		return "", err
	}
	return projectionKey, nil
}

// RegisterConversationStatusSteerTx 在 Codex 确认 steer 后登记最新输入卡。
func RegisterConversationStatusSteerTx(ctx context.Context, tx *sql.Tx, runID,
	conversationID uuid.UUID, guildID, messageID string,
) error {
	key := "conversation:" + conversationID.String() + ":message:" + messageID
	initial, err := ensureInitialConversationStatusTx(ctx, tx, runID, "")
	if err != nil {
		return err
	}
	if initial == "" {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM discord_projections
			WHERE guild_id=$1 AND projection_key=$2)`, guildID, key).Scan(&exists); err != nil || !exists {
			return err
		}
		return registerInitialConversationStatusTx(ctx, tx, runID, guildID, key)
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM discord_turn_status_cards
		WHERE run_id = $1 AND projection_key = $2)`, runID, key).Scan(&exists); err != nil || exists {
		return err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(revision),0)+1
		FROM discord_turn_status_cards WHERE run_id = $1`, runID).Scan(&revision); err != nil {
		return err
	}
	var projectedMessageID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(message_id,'') FROM discord_projections
		WHERE guild_id = $1 AND projection_key = $2`, guildID, key).Scan(&projectedMessageID); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO discord_turn_status_cards
		(run_id,guild_id,projection_key,revision,role,boundary_client_id)
		VALUES ($1,$2,$3,$4,'pending',$5)`, runID, guildID, key, revision, messageID); err != nil {
		return err
	}
	if err := resolveStoredConversationStatusBoundaryTx(ctx, tx, runID, messageID); err != nil {
		return err
	}
	if err := freezePreviousConversationStatusCardTx(ctx, tx, runID, revision); err != nil {
		return err
	}
	if projectedMessageID != "" {
		return promoteConversationStatusCardTx(ctx, tx, runID, guildID, key)
	}
	return nil
}

func promotePendingConversationStatusTx(ctx context.Context, tx *sql.Tx, guildID,
	projectionKey string,
) error {
	rows, err := tx.QueryContext(ctx, `SELECT run_id FROM discord_turn_status_cards
		WHERE guild_id = $1 AND projection_key = $2 AND role = 'pending'
		ORDER BY revision`, guildID, projectionKey)
	if err != nil {
		return err
	}
	var runs []uuid.UUID
	for rows.Next() {
		var runID uuid.UUID
		if err := rows.Scan(&runID); err != nil {
			_ = rows.Close()
			return err
		}
		runs = append(runs, runID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, runID := range runs {
		if err := promoteConversationStatusCardTx(ctx, tx, runID, guildID, projectionKey); err != nil {
			return err
		}
	}
	return nil
}

func promoteConversationStatusCardTx(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	guildID, targetKey string,
) error {
	if err := tx.QueryRowContext(ctx, `SELECT id FROM codex_turn_runs
		WHERE id = $1 FOR UPDATE`, runID).Scan(&runID); err != nil {
		return err
	}
	var targetRevision int64
	var targetMessageID string
	if err := tx.QueryRowContext(ctx, `SELECT card.revision,
		COALESCE(projection.message_id,'') FROM discord_turn_status_cards card
		JOIN discord_projections projection ON projection.guild_id=card.guild_id
			AND projection.projection_key=card.projection_key
		WHERE card.run_id=$1 AND card.projection_key=$2 FOR UPDATE OF card, projection`,
		runID, targetKey).Scan(&targetRevision, &targetMessageID); err != nil {
		return err
	}
	if targetMessageID == "" {
		return nil
	}
	var currentKey string
	var currentRevision int64
	err := tx.QueryRowContext(ctx, `SELECT projection_key, revision
		FROM discord_turn_status_cards WHERE run_id=$1 AND role='current' FOR UPDATE`, runID).
		Scan(&currentKey, &currentRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) || targetRevision > currentRevision {
		if currentKey != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE discord_turn_status_cards SET role='history',
				updated_at=now() WHERE run_id=$1 AND projection_key=$2`, runID, currentKey); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE discord_turn_status_cards SET role='current',
			updated_at=now() WHERE run_id=$1 AND projection_key=$2`, runID, targetKey); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE discord_turn_status_cards SET role='history',
		updated_at=now() WHERE run_id=$1 AND projection_key=$2`, runID, targetKey); err != nil {
		return err
	}
	return refreshConversationStatusCardTx(ctx, tx, runID, guildID, targetKey)
}

func updateStatusProjectionTx(ctx context.Context, tx *sql.Tx, guildID, key string,
	desired any,
) error {
	var threadID, messageID string
	err := tx.QueryRowContext(ctx, `UPDATE discord_projections SET desired_payload=$3,
		desired_version=desired_version+1, updated_at=now()
		WHERE guild_id=$1 AND projection_key=$2
		RETURNING resource_id, COALESCE(message_id,'')`, guildID, key, mustJSON(desired)).
		Scan(&threadID, &messageID)
	if err != nil {
		return err
	}
	var payload map[string]any
	encoded := mustJSON(desired)
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return err
	}
	payload["channelId"] = threadID
	if messageID == "" {
		return enqueueDiscordOutbox(ctx, tx, "projection:"+key, "message.create",
			"channels/"+threadID+"/messages", payload, conversationStatusNonceForKey(key))
	}
	payload["messageId"] = messageID
	return enqueueDiscordOutbox(ctx, tx, "projection:"+key, "message.update",
		"channels/"+threadID+"/messages/"+messageID, payload, "")
}
