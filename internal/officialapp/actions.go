package officialapp

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/security"
)

type ThreadAction struct {
	ID             uuid.UUID
	WorkspaceID    uuid.UUID
	ConversationID uuid.UUID
	SourceOrder    string
	Action         string
	AttemptCount   int
	LeaseToken     string
}

func EnqueueThreadActionTx(ctx context.Context, tx *sql.Tx, workspaceID,
	conversationID uuid.UUID, sourceOrder, idempotencyKey, action string,
) (uuid.UUID, bool, error) {
	if workspaceID == uuid.Nil || conversationID == uuid.Nil || idempotencyKey == "" ||
		(action != "interrupt" && action != "archive" && action != "unarchive") {
		return uuid.Nil, false, errors.New("官方 Thread action 参数无效")
	}
	if _, err := strconv.ParseUint(sourceOrder, 10, 64); err != nil {
		return uuid.Nil, false, errors.New("官方 Thread action 顺序不是有效 Snowflake")
	}
	id := uuid.New()
	result, err := tx.ExecContext(ctx, `INSERT INTO official_thread_actions(
		id,workspace_id,conversation_id,source_order,idempotency_key,action)
		VALUES($1,$2,$3,$4::numeric,$5,$6)
		ON CONFLICT(idempotency_key) DO NOTHING`, id, workspaceID, conversationID,
		sourceOrder, idempotencyKey, action)
	if err != nil {
		return uuid.Nil, false, err
	}
	inserted, _ := result.RowsAffected()
	if inserted == 1 {
		return id, true, nil
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM official_thread_actions
		WHERE idempotency_key=$1`, idempotencyKey).Scan(&id)
	return id, false, err
}

func ClaimNextThreadAction(ctx context.Context, db *sql.DB, workspaceID uuid.UUID,
	lease time.Duration,
) (*ThreadAction, error) {
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE official_thread_actions SET
		status=CASE WHEN attempt_count<3 THEN 'queued' ELSE 'failed' END,
		lease_token_hash=NULL,lease_expires_at=NULL,available_at=now(),updated_at=now(),
		last_error=COALESCE(last_error,'action lease expired')
		WHERE workspace_id=$1 AND status='applying' AND lease_expires_at<now()`,
		workspaceID)
	if err != nil {
		return nil, err
	}
	var item ThreadAction
	err = tx.QueryRowContext(ctx, `SELECT id,workspace_id,conversation_id,
		source_order::text,action,attempt_count FROM official_thread_actions action
		WHERE action.workspace_id=$1 AND action.status='queued'
		  AND action.available_at<=now() AND action.attempt_count<3
		  AND NOT EXISTS(SELECT 1 FROM official_thread_actions predecessor
			WHERE predecessor.conversation_id=action.conversation_id
			  AND predecessor.source_order<action.source_order
			  AND predecessor.status IN ('queued','applying'))
		  AND NOT EXISTS(SELECT 1 FROM official_turn_submissions predecessor
			WHERE predecessor.conversation_id=action.conversation_id
			  AND predecessor.source_order<action.source_order
			  AND predecessor.status IN ('queued','submitting','ambiguous'))
		ORDER BY action.source_order,action.created_at,action.id
		FOR UPDATE SKIP LOCKED LIMIT 1`, workspaceID).Scan(&item.ID, &item.WorkspaceID,
		&item.ConversationID, &item.SourceOrder, &item.Action, &item.AttemptCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return nil, err
	}
	item.LeaseToken = token
	_, err = tx.ExecContext(ctx, `UPDATE official_thread_actions SET status='applying',
		attempt_count=attempt_count+1,lease_token_hash=$2,lease_expires_at=$3,
		last_error=NULL,updated_at=now() WHERE id=$1`, item.ID, security.Digest(token),
		time.Now().UTC().Add(lease))
	if err != nil {
		return nil, err
	}
	item.AttemptCount++
	return &item, tx.Commit()
}

func CompleteThreadAction(ctx context.Context, db *sql.DB, item ThreadAction) error {
	result, err := db.ExecContext(ctx, `UPDATE official_thread_actions SET
		status='completed',lease_token_hash=NULL,lease_expires_at=NULL,last_error=NULL,
		completed_at=now(),updated_at=now() WHERE id=$1 AND status='applying'
		AND lease_token_hash=$2`, item.ID, security.Digest(item.LeaseToken))
	return changedOne(result, err)
}

func DeferThreadAction(ctx context.Context, db *sql.DB, item ThreadAction,
	delay time.Duration,
) error {
	if delay <= 0 {
		delay = time.Second
	}
	result, err := db.ExecContext(ctx, `UPDATE official_thread_actions SET status='queued',
		attempt_count=GREATEST(attempt_count-1,0),available_at=now()+$3::interval,
		lease_token_hash=NULL,lease_expires_at=NULL,updated_at=now()
		WHERE id=$1 AND status='applying' AND lease_token_hash=$2`, item.ID,
		security.Digest(item.LeaseToken), interval(delay))
	return changedOne(result, err)
}

func FailThreadActionTx(ctx context.Context, tx *sql.Tx, item ThreadAction,
	cause error,
) (bool, error) {
	status := "failed"
	if item.AttemptCount < 3 {
		status = "queued"
	}
	result, err := tx.ExecContext(ctx, `UPDATE official_thread_actions SET status=$3,
		available_at=CASE WHEN $3='queued' THEN now()+interval '1 second' ELSE available_at END,
		lease_token_hash=NULL,lease_expires_at=NULL,last_error=$4,updated_at=now()
		WHERE id=$1 AND status='applying' AND lease_token_hash=$2`, item.ID,
		security.Digest(item.LeaseToken), status, cause.Error())
	return status == "failed", changedOne(result, err)
}

func interval(value time.Duration) string {
	return strconv.FormatFloat(value.Seconds(), 'f', 6, 64) + " seconds"
}
