package officialapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/security"
)

const WakeupChannel = "tyrs-hand:official-app:wakeup"

type EnqueueRequest struct {
	WorkspaceID           uuid.UUID
	ConversationID        uuid.UUID
	PlanActionID          uuid.UUID
	SourceType            string
	SourceOrder           string
	DiscordMessageID      string
	ClientMessageID       string
	Instruction           string
	DisplayInstruction    string
	Input                 []UserInput
	Preferences           Preferences
	AdditionalContext     map[string]ports.AdditionalContextEntry
	DeveloperInstructions string
}

func EnqueueTx(ctx context.Context, tx *sql.Tx, request EnqueueRequest) (uuid.UUID,
	bool, error,
) {
	if request.WorkspaceID == uuid.Nil || request.ConversationID == uuid.Nil ||
		request.ClientMessageID == "" || request.Instruction == "" ||
		(request.SourceType != "discord_message" && request.SourceType != "discord_plan") {
		return uuid.Nil, false, errors.New("官方提交参数无效")
	}
	if _, err := strconv.ParseUint(request.SourceOrder, 10, 64); err != nil {
		return uuid.Nil, false, errors.New("官方提交顺序不是有效 Snowflake")
	}
	input, err := json.Marshal(request.Input)
	if err != nil {
		return uuid.Nil, false, err
	}
	preferences, err := json.Marshal(request.Preferences)
	if err != nil {
		return uuid.Nil, false, err
	}
	additionalContext, err := json.Marshal(request.AdditionalContext)
	if err != nil {
		return uuid.Nil, false, err
	}
	id := uuid.New()
	result, err := tx.ExecContext(ctx, `INSERT INTO official_turn_submissions(
		id,workspace_id,conversation_id,plan_action_id,source_type,source_order,
		discord_message_id,client_user_message_id,instruction,display_instruction,
		input,preferences,additional_context,developer_instructions)
		VALUES ($1,$2,$3,NULLIF($4::text,'')::uuid,$5,$6::numeric,
		NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT(workspace_id,client_user_message_id) DO NOTHING`, id,
		request.WorkspaceID, request.ConversationID, optionalUUID(request.PlanActionID),
		request.SourceType, request.SourceOrder, request.DiscordMessageID,
		request.ClientMessageID, request.Instruction, request.DisplayInstruction,
		input, preferences, additionalContext, request.DeveloperInstructions)
	if err != nil {
		return uuid.Nil, false, err
	}
	inserted, _ := result.RowsAffected()
	if inserted == 1 {
		return id, true, nil
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM official_turn_submissions
		WHERE workspace_id=$1 AND client_user_message_id=$2`, request.WorkspaceID,
		request.ClientMessageID).Scan(&id)
	return id, false, err
}

func optionalUUID(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

type Submission struct {
	ID                    uuid.UUID
	WorkspaceID           uuid.UUID
	ConversationID        uuid.UUID
	SourceType            string
	SourceOrder           string
	DiscordMessageID      string
	ClientMessageID       string
	Instruction           string
	DisplayInstruction    string
	Input                 []UserInput
	Preferences           Preferences
	AdditionalContext     map[string]ports.AdditionalContextEntry
	DeveloperInstructions string
	Status                string
	AttemptCount          int
	ThreadID              string
	TurnID                string
	LeaseToken            string
	LeaseExpiresAt        time.Time
}

func ClaimNext(ctx context.Context, db *sql.DB, workspaceID uuid.UUID,
	lease time.Duration,
) (*Submission, error) {
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE official_turn_submissions SET status='queued',
		lease_token_hash=NULL,lease_expires_at=NULL,available_at=now(),updated_at=now()
		WHERE workspace_id=$1 AND status='submitting' AND lease_expires_at<now()
		AND attempt_count<2`, workspaceID)
	if err != nil {
		return nil, err
	}
	var item Submission
	var input, preferences, additionalContext json.RawMessage
	err = tx.QueryRowContext(ctx, `SELECT submission.id,submission.workspace_id,
		submission.conversation_id,submission.source_type,submission.source_order::text,
		COALESCE(submission.discord_message_id,''),submission.client_user_message_id,
		submission.instruction,submission.display_instruction,submission.input,
		submission.preferences,submission.additional_context,
		submission.developer_instructions,submission.status,submission.attempt_count,
		COALESCE(submission.thread_id,''),COALESCE(submission.turn_id,'')
		FROM official_turn_submissions submission
		WHERE submission.workspace_id=$1 AND submission.status='queued'
		  AND submission.available_at<=now() AND submission.attempt_count<2
		  AND NOT EXISTS(SELECT 1 FROM official_submission_attachments attachment
			LEFT JOIN client_materializations materialization
				ON materialization.id=attachment.materialization_id
			WHERE attachment.submission_id=submission.id
			  AND (materialization.id IS NULL OR materialization.status<>'completed'))
		  AND NOT EXISTS(SELECT 1 FROM official_turn_submissions predecessor
			WHERE predecessor.conversation_id=submission.conversation_id
			  AND predecessor.source_order<submission.source_order
			  AND predecessor.status IN ('queued','submitting','ambiguous'))
		  AND NOT EXISTS(SELECT 1 FROM official_thread_actions predecessor
			WHERE predecessor.conversation_id=submission.conversation_id
			  AND predecessor.source_order<submission.source_order
			  AND predecessor.status IN ('queued','applying'))
		ORDER BY submission.source_order,submission.created_at,submission.id
		FOR UPDATE SKIP LOCKED LIMIT 1`, workspaceID).Scan(&item.ID, &item.WorkspaceID,
		&item.ConversationID, &item.SourceType, &item.SourceOrder, &item.DiscordMessageID,
		&item.ClientMessageID, &item.Instruction, &item.DisplayInstruction, &input,
		&preferences, &additionalContext, &item.DeveloperInstructions, &item.Status,
		&item.AttemptCount, &item.ThreadID, &item.TurnID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(input, &item.Input); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(preferences, &item.Preferences); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(additionalContext, &item.AdditionalContext); err != nil {
		return nil, err
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return nil, err
	}
	item.LeaseToken = token
	item.LeaseExpiresAt = time.Now().UTC().Add(lease)
	_, err = tx.ExecContext(ctx, `UPDATE official_turn_submissions SET status='submitting',
		attempt_count=attempt_count+1,lease_token_hash=$2,lease_expires_at=$3,
		last_error=NULL,updated_at=now() WHERE id=$1`, item.ID, security.Digest(token),
		item.LeaseExpiresAt)
	if err != nil {
		return nil, err
	}
	item.Status = "submitting"
	item.AttemptCount++
	return &item, tx.Commit()
}

func Complete(ctx context.Context, db *sql.DB, item Submission, threadID, turnID string) error {
	result, err := db.ExecContext(ctx, `UPDATE official_turn_submissions SET
		status='submitted',thread_id=$4,turn_id=$5,lease_token_hash=NULL,
		lease_expires_at=NULL,last_error=NULL,submitted_at=now(),updated_at=now()
		WHERE id=$1 AND status='submitting' AND lease_token_hash=$2
		AND lease_expires_at>now() AND workspace_id=$3`, item.ID,
		security.Digest(item.LeaseToken), item.WorkspaceID, threadID, turnID)
	return changedOne(result, err)
}

func MarkAmbiguous(ctx context.Context, db *sql.DB, item Submission, cause error) error {
	result, err := db.ExecContext(ctx, `UPDATE official_turn_submissions SET
		status='ambiguous',thread_id=NULLIF($4,''),lease_token_hash=NULL,
		lease_expires_at=NULL,last_error=$5,updated_at=now()
		WHERE id=$1 AND status='submitting' AND lease_token_hash=$2 AND workspace_id=$3`,
		item.ID, security.Digest(item.LeaseToken), item.WorkspaceID, item.ThreadID,
		cause.Error())
	return changedOne(result, err)
}

func RetryOrFail(ctx context.Context, db *sql.DB, item Submission, cause error) error {
	status := "failed"
	if item.AttemptCount < 2 {
		status = "queued"
	}
	result, err := db.ExecContext(ctx, `UPDATE official_turn_submissions SET status=$4,
		available_at=CASE WHEN $4='queued' THEN now()+interval '1 second' ELSE available_at END,
		lease_token_hash=NULL,lease_expires_at=NULL,last_error=$5,updated_at=now()
		WHERE id=$1 AND status='submitting' AND lease_token_hash=$2 AND workspace_id=$3`,
		item.ID, security.Digest(item.LeaseToken), item.WorkspaceID, status, cause.Error())
	return changedOne(result, err)
}

func changedOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("官方提交 lease 已失效")
	}
	return nil
}
