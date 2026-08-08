package codexcontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/security"
)

var (
	ErrLeaseLost         = errors.New("codex control 租约已经失效")
	ErrControlTerminated = errors.New("codex control 已经进入错误终态")
	ErrControlArchived   = errors.New("codex 会话已经归档或正在归档")
	ErrInvalidSource     = errors.New("GitHub Job 调度只支持 github_work_item")
)

type Repository struct {
	db            *sql.DB
	leaseDuration time.Duration
	maxSteers     int
	maxAttempts   int
}

func NewRepository(db *sql.DB, leaseDuration time.Duration, limits ...int) *Repository {
	maxSteers, maxAttempts := 5, 3
	if len(limits) > 0 && limits[0] > 0 {
		maxSteers = limits[0]
	}
	if len(limits) > 1 && limits[1] > 0 {
		maxAttempts = limits[1]
	}
	return &Repository{db: db, leaseDuration: leaseDuration,
		maxSteers: maxSteers, maxAttempts: maxAttempts}
}

func (r *Repository) Enqueue(ctx context.Context, tx *sql.Tx,
	request EnqueueRequest,
) (uuid.UUID, bool, error) {
	if request.SourceType != SourceGitHub {
		return uuid.Nil, false, ErrInvalidSource
	}
	if request.WorkItemID == uuid.Nil || request.RepositoryID == uuid.Nil ||
		request.AgentProfileID == uuid.Nil || request.IdempotencyKey == "" ||
		request.Instruction == "" {
		return uuid.Nil, false, errors.New("GitHub Job 参数不完整")
	}
	if request.ReplyPolicy == "" {
		request.ReplyPolicy = "silent"
	}
	var workerID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT worker_id::text FROM work_items
		WHERE id=$1 FOR UPDATE`, request.WorkItemID).Scan(&workerID); err != nil {
		return uuid.Nil, false, err
	}
	if !workerID.Valid {
		_ = tx.QueryRowContext(ctx, `SELECT value->>'workerId' FROM platform_settings
			WHERE setting_key='worker.default.github'`).Scan(&workerID)
		if workerID.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE work_items SET worker_id=$2,
				updated_at=now() WHERE id=$1 AND worker_id IS NULL`, request.WorkItemID,
				workerID.String); err != nil {
				return uuid.Nil, false, err
			}
		}
	}
	var controlID uuid.UUID
	err := tx.QueryRowContext(ctx, `INSERT INTO codex_thread_controls
		(source_type,work_item_id,repository_id,agent_profile_id,worker_id)
		VALUES ('github_work_item',$1,$2,$3,NULLIF($4,'')::uuid)
		ON CONFLICT(work_item_id,agent_profile_id) WHERE work_item_id IS NOT NULL
		DO UPDATE SET worker_id=COALESCE(codex_thread_controls.worker_id,
			EXCLUDED.worker_id),updated_at=now() RETURNING id`, request.WorkItemID,
		request.RepositoryID, request.AgentProfileID, workerID.String).Scan(&controlID)
	if err != nil {
		return uuid.Nil, false, err
	}
	var status, lifecycle string
	if err = tx.QueryRowContext(ctx, `SELECT status,lifecycle_state
		FROM codex_thread_controls WHERE id=$1`, controlID).Scan(&status, &lifecycle); err != nil {
		return uuid.Nil, false, err
	}
	if status == "error" {
		return uuid.Nil, false, ErrControlTerminated
	}
	if lifecycle != "active" {
		return uuid.Nil, false, ErrControlArchived
	}
	var sequence int64
	if err = tx.QueryRowContext(ctx, `UPDATE codex_thread_controls SET
		next_sequence_no=next_sequence_no+1,updated_at=now()
		WHERE id=$1 RETURNING next_sequence_no-1`, controlID).Scan(&sequence); err != nil {
		return uuid.Nil, false, err
	}
	initialStatus := "queued"
	if !workerID.Valid || workerID.String == "" {
		initialStatus = "placement_pending"
	}
	var intentID uuid.UUID
	err = tx.QueryRowContext(ctx, `INSERT INTO codex_turn_intents(
		control_id,sequence_no,operation,behavior,source_type,work_item_id,repository_id,
		agent_profile_id,webhook_delivery_id,trigger_rule_id,trigger_evidence,idempotency_key,
		instruction,skills,allowed_tools,dangerous_actions,priority,actor_login,
		actor_permission,reply_policy,reply_status,status)
		VALUES($1,$2,'turn_input','steer_if_active','github_work_item',$3,$4,$5,
		NULLIF($6::text,'')::uuid,NULLIF($7::text,'')::uuid,$8,$9,$10,$11,$12,$13,
		$14,$15,$16,$17,CASE WHEN $17='required' THEN 'pending' ELSE 'skipped' END,$18)
		ON CONFLICT(idempotency_key) DO NOTHING RETURNING id`, controlID, sequence,
		request.WorkItemID, request.RepositoryID, request.AgentProfileID,
		nilUUID(request.WebhookDeliveryID), nilUUID(request.TriggerRuleID),
		defaultJSON(request.TriggerEvidence), request.IdempotencyKey, request.Instruction,
		encode(request.Skills), encode(request.AllowedTools), encode(request.DangerousActions),
		request.Priority, request.ActorLogin, request.ActorPermission, request.ReplyPolicy,
		initialStatus).Scan(&intentID)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return intentID, err == nil, err
}

func nilUUID(value uuid.UUID) string {
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}

func encode(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

func encodeOptional(value any) []byte {
	if value == nil {
		return nil
	}
	return encode(value)
}

func defaultJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func interval(value time.Duration) string { return fmt.Sprintf("%f seconds", value.Seconds()) }

func (r *Repository) Claim(ctx context.Context, leaseOwner string) (*ClaimedControl, error) {
	return r.claimGitHub(ctx, leaseOwner, "")
}

func (r *Repository) ClaimSource(ctx context.Context, leaseOwner,
	sourceType string,
) (*ClaimedControl, error) {
	if sourceType != "" && sourceType != SourceGitHub {
		return nil, ErrInvalidSource
	}
	return r.claimGitHub(ctx, leaseOwner, "")
}

func (r *Repository) ClaimWorker(ctx context.Context, leaseOwner,
	sourceType string, workerID uuid.UUID,
) (*ClaimedControl, error) {
	if sourceType != SourceGitHub || workerID == uuid.Nil {
		return nil, ErrInvalidSource
	}
	return r.claimGitHub(ctx, leaseOwner, workerID.String())
}

func (r *Repository) claimGitHub(ctx context.Context, leaseOwner,
	workerID string,
) (*ClaimedControl, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	leaseToken, err := security.RandomToken(32)
	if err != nil {
		return nil, err
	}
	capability, err := security.RandomToken(32)
	if err != nil {
		return nil, err
	}
	var controlID uuid.UUID
	var oldStatus string
	err = tx.QueryRowContext(ctx, `SELECT control.id,control.status
		FROM codex_thread_controls control
		WHERE control.source_type='github_work_item' AND control.status<>'error'
		  AND control.lifecycle_state='active'
		  AND ($2='' OR control.worker_id=$2::uuid)
		  AND (control.lease_expires_at IS NULL OR control.lease_expires_at<now())
		  AND EXISTS(SELECT 1 FROM codex_turn_intents intent
			WHERE intent.control_id=control.id AND intent.source_type='github_work_item'
			  AND intent.status IN ('queued','retry_wait','reconciling')
			  AND intent.available_at<=now() AND intent.attempt_count<$1)
		ORDER BY COALESCE(control.next_wakeup_at,control.created_at),control.created_at
		FOR UPDATE SKIP LOCKED LIMIT 1`, r.maxAttempts, workerID).
		Scan(&controlID, &oldStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, fmt.Errorf("领取 GitHub Job: %w", err)
	}
	var claimed ClaimedControl
	var skillsJSON, toolsJSON, dangerousJSON []byte
	var runModel, runEffort, runTier sql.NullString
	var settingsRevision int64
	err = tx.QueryRowContext(ctx, `SELECT intent.id,intent.sequence_no,
		intent.work_item_id,intent.repository_id,intent.agent_profile_id,
		intent.instruction,intent.skills,intent.allowed_tools,intent.dangerous_actions,
		intent.actor_login,intent.actor_permission,intent.reply_policy,intent.reply_status,
		intent.attempt_count+1,$2::integer,COALESCE(intent.codex_submission_id,''),
		COALESCE(intent.confirmed_codex_turn_id,''),intent.created_at,
		COALESCE(control.external_thread_id,''),control.lease_epoch+1,
		control.collaboration_mode,control.model,control.reasoning_effort,
		control.service_tier,control.settings_revision
		FROM codex_turn_intents intent JOIN codex_thread_controls control
			ON control.id=intent.control_id
		WHERE intent.control_id=$1 AND intent.source_type='github_work_item'
		  AND intent.status IN ('queued','retry_wait','reconciling')
		  AND intent.available_at<=now() AND intent.attempt_count<$2
		ORDER BY intent.sequence_no FOR UPDATE OF intent LIMIT 1`, controlID,
		r.maxAttempts).Scan(&claimed.ID, &claimed.Sequence, &claimed.WorkItemID,
		&claimed.RepositoryID, &claimed.AgentProfileID, &claimed.Instruction,
		&skillsJSON, &toolsJSON, &dangerousJSON, &claimed.ActorLogin,
		&claimed.ActorPermission, &claimed.ReplyPolicy, &claimed.ReplyStatus,
		&claimed.Attempt, &claimed.MaxAttempts, &claimed.SubmissionID,
		&claimed.ConfirmedTurnID, &claimed.CreatedAt, &claimed.ExternalThreadID,
		&claimed.LeaseEpoch, &claimed.CollaborationMode, &runModel, &runEffort,
		&runTier, &settingsRevision)
	if err != nil {
		return nil, err
	}
	claimed.ControlID = controlID
	claimed.SourceType = SourceGitHub
	claimed.Operation = "turn_input"
	claimed.Recovering = oldStatus == "reconciling" || claimed.SubmissionID != "" ||
		claimed.ConfirmedTurnID != ""
	for raw, target := range map[*[]byte]*[]string{
		&skillsJSON: &claimed.Skills, &toolsJSON: &claimed.AllowedTools,
		&dangerousJSON: &claimed.DangerousActions,
	} {
		if err = json.Unmarshal(*raw, target); err != nil {
			return nil, err
		}
	}
	claimed.LeaseToken = leaseToken
	claimed.LeaseExpiresAt = time.Now().Add(r.leaseDuration)
	_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status='dispatching',
		active_intent_id=$2,lease_owner=$3,lease_token=$4,lease_epoch=$5,
		lease_expires_at=now()+$6::interval,heartbeat_at=now(),updated_at=now()
		WHERE id=$1`, controlID, claimed.ID, leaseOwner, security.Digest(leaseToken),
		claimed.LeaseEpoch, interval(r.leaseDuration))
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status='dispatching',
		attempt_count=attempt_count+1,max_attempts=$2,
		dispatched_at=COALESCE(dispatched_at,now()),updated_at=now() WHERE id=$1`,
		claimed.ID, r.maxAttempts)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO codex_turn_runs(
		control_id,primary_intent_id,attempt,lease_owner,lease_epoch,capability_hash,
		active_slot,max_append_count,worker_id,collaboration_mode,model,
		reasoning_effort,service_tier,settings_revision)
		VALUES($1,$2,$3,$4,$5,$6,1,$7,NULLIF($8,'')::uuid,$9,$10,$11,$12,$13)
		RETURNING id`, controlID, claimed.ID, claimed.Attempt, leaseOwner,
		claimed.LeaseEpoch, security.Digest(capability), r.maxSteers, workerID,
		claimed.CollaborationMode, runModel, runEffort, runTier, settingsRevision).
		Scan(&claimed.RunID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	claimed.Capability = capability
	return &claimed, nil
}

func (r *Repository) Heartbeat(ctx context.Context, claimed *ClaimedControl) error {
	result, err := r.db.ExecContext(ctx, `WITH updated_control AS (
		UPDATE codex_thread_controls SET lease_expires_at=now()+$4::interval,
			heartbeat_at=now(),status=CASE WHEN status='reconciling' THEN 'active'
			ELSE status END,last_error_code=CASE WHEN status='reconciling' THEN NULL
			ELSE last_error_code END,last_error_message=CASE WHEN status='reconciling'
			THEN NULL ELSE last_error_message END,updated_at=now()
		WHERE id=$1 AND source_type='github_work_item' AND lease_token=$2
		  AND lease_epoch=$3 AND active_intent_id=$5
		  AND status IN ('dispatching','active','stopping','reconciling') RETURNING id)
	UPDATE codex_turn_runs SET heartbeat_at=now() WHERE id=$6
	  AND control_id=(SELECT id FROM updated_control) AND active_slot=1`,
		claimed.ControlID, security.Digest(claimed.LeaseToken), claimed.LeaseEpoch,
		interval(r.leaseDuration), claimed.ID, claimed.RunID)
	return requireOne(result, err)
}

func (r *Repository) SetThread(ctx context.Context, claimed *ClaimedControl,
	threadID string,
) error {
	result, err := r.db.ExecContext(ctx, `UPDATE codex_thread_controls SET
		external_thread_id=$4,status='active',remote_status='idle',
		last_error_code=NULL,last_error_message=NULL,updated_at=now()
		WHERE id=$1 AND source_type='github_work_item' AND lease_token=$2
		AND lease_epoch=$3`, claimed.ControlID, security.Digest(claimed.LeaseToken),
		claimed.LeaseEpoch, threadID)
	return requireOne(result, err)
}

func (r *Repository) RecordSubmission(ctx context.Context, claimed *ClaimedControl,
	submissionID string,
) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = r.fence(ctx, tx, claimed); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET
		status='awaiting_confirmation',codex_submission_id=$2,updated_at=now()
		WHERE id=$1 AND source_type='github_work_item'`, claimed.ID, submissionID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status='running',
			codex_submission_id=$2,heartbeat_at=now() WHERE id=$1`, claimed.RunID,
			submissionID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET
			remote_status='dispatching',active_client_id=$2,updated_at=now() WHERE id=$1`,
			claimed.ControlID, claimed.ID.String())
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ConfirmTurn(ctx context.Context, claimed *ClaimedControl,
	turnID string,
) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = r.fence(ctx, tx, claimed); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status='running',
		confirmed_codex_turn_id=$2,confirmed_at=COALESCE(confirmed_at,now()),updated_at=now()
		WHERE id=$1 AND source_type='github_work_item'
		AND (confirmed_codex_turn_id IS NULL OR confirmed_codex_turn_id=$2)`,
		claimed.ID, turnID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status='running',
			confirmed_codex_turn_id=$2,heartbeat_at=now() WHERE id=$1`, claimed.RunID,
			turnID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status='active',
			remote_status='active',active_codex_turn_id=$2,updated_at=now() WHERE id=$1`,
			claimed.ControlID, turnID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) Complete(ctx context.Context, claimed *ClaimedControl,
	result TurnResult,
) error {
	return r.finish(ctx, claimed, IntentCompleted, "", "", result, nil)
}

func (r *Repository) Cancel(ctx context.Context, claimed *ClaimedControl,
	code, message string,
) error {
	return r.finish(ctx, claimed, IntentCanceled, code, message, TurnResult{}, nil)
}

func (r *Repository) Fail(ctx context.Context, claimed *ClaimedControl,
	code string, cause error,
) error {
	return r.FailWithCodexError(ctx, claimed, code, cause, nil)
}

func (r *Repository) FailWithCodexError(ctx context.Context, claimed *ClaimedControl,
	code string, cause error, codexError any,
) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return r.finish(ctx, claimed, IntentFailed, code, message, TurnResult{},
		encodeOptional(codexError))
}

func (r *Repository) Reconcile(ctx context.Context, claimed *ClaimedControl,
	code string, cause error,
) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = r.fence(ctx, tx, claimed); err != nil {
		return err
	}
	intentStatus, controlStatus := IntentRetryWait, "reconciling"
	if claimed.Attempt >= claimed.MaxAttempts {
		intentStatus, controlStatus = IntentFailed, "idle"
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status=$2,
		last_error_code=$3,last_error_message=$4,
		available_at=CASE WHEN $2='retry_wait' THEN now()+interval '15 seconds' ELSE now() END,
		finished_at=CASE WHEN $2='failed' THEN now() ELSE NULL END,updated_at=now()
		WHERE id=$1 AND source_type='github_work_item'`, claimed.ID, intentStatus,
		code, message)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status='failed',
			active_slot=NULL,error_code=$2,error_message=$3,finished_at=now() WHERE id=$1`,
			claimed.RunID, code, message)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status=$2,
			active_intent_id=NULL,remote_status=CASE WHEN $2='idle' THEN 'idle'
			ELSE remote_status END,active_codex_turn_id=CASE WHEN $2='idle' THEN NULL
			ELSE active_codex_turn_id END,active_client_id=CASE WHEN $2='idle' THEN NULL
			ELSE active_client_id END,lease_owner=NULL,lease_token=NULL,
			lease_expires_at=NULL,last_error_code=$3,last_error_message=$4,
			next_wakeup_at=CASE WHEN $2='reconciling' THEN now()+interval '15 seconds'
			ELSE NULL END,updated_at=now() WHERE id=$1`, claimed.ControlID,
			controlStatus, code, message)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) finish(ctx context.Context, claimed *ClaimedControl,
	status IntentStatus, code, message string, turnResult TurnResult,
	codexError []byte,
) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = r.fence(ctx, tx, claimed); err != nil {
		return err
	}
	var resultJSON any
	if status == IntentCompleted {
		resultJSON = encode(turnResult)
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status=$2,result=$3,
		last_error_code=NULLIF($4,''),last_error_message=NULLIF($5,''),finished_at=now(),
		result_delivery_status=CASE WHEN $2='completed' THEN 'skipped'
			ELSE result_delivery_status END,
		result_delivered_at=CASE WHEN $2='completed' THEN now() ELSE result_delivered_at END,
		result_delivery_available_at=now(),updated_at=now()
		WHERE id=$1 AND source_type='github_work_item'`, claimed.ID, status, resultJSON,
		code, message)
	if err == nil {
		runStatus := string(status)
		if codexError == nil {
			_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status=$2,
				active_slot=NULL,error_code=NULLIF($3,''),error_message=NULLIF($4,''),
				finished_at=now() WHERE id=$1`, claimed.RunID, runStatus, code, message)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status=$2,
				active_slot=NULL,error_code=NULLIF($3,''),error_message=NULLIF($4,''),
				codex_error=$5,finished_at=now() WHERE id=$1`, claimed.RunID, runStatus,
				code, message, codexError)
		}
	}
	if err == nil {
		controlStatus := "idle"
		if status == IntentFailed {
			controlStatus = "error"
		}
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status=$2,
			active_intent_id=CASE WHEN $2='error' THEN active_intent_id ELSE NULL END,
			remote_status='idle',active_codex_turn_id=NULL,active_client_id=NULL,
			lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
			last_error_code=NULLIF($3,''),last_error_message=NULLIF($4,''),updated_at=now()
			WHERE id=$1 AND source_type='github_work_item'`, claimed.ControlID,
			controlStatus, code, message)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ReplySatisfied(ctx context.Context,
	claimed *ClaimedControl,
) (bool, error) {
	if claimed.ReplyPolicy != "required" {
		return true, nil
	}
	var delivered bool
	err := r.db.QueryRowContext(ctx, `SELECT reply_status='delivered'
		FROM codex_turn_intents WHERE id=$1 AND source_type='github_work_item'`,
		claimed.ID).Scan(&delivered)
	return delivered, err
}

func (r *Repository) fence(ctx context.Context, tx *sql.Tx,
	claimed *ClaimedControl,
) error {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM codex_thread_controls
		WHERE id=$1 AND source_type='github_work_item' AND lease_token=$2
		AND lease_epoch=$3 AND active_intent_id=$4)`, claimed.ControlID,
		security.Digest(claimed.LeaseToken), claimed.LeaseEpoch, claimed.ID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrLeaseLost
	}
	return nil
}

func (r *Repository) RequeueExpired(ctx context.Context) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT control.id,control.active_intent_id
		FROM codex_thread_controls control JOIN codex_turn_intents intent
			ON intent.id=control.active_intent_id
		WHERE control.source_type='github_work_item'
		  AND (control.lease_expires_at<now() OR EXISTS(
			SELECT 1 FROM codex_turn_runs run WHERE run.control_id=control.id
			AND run.active_slot=1 AND run.finished_at IS NOT NULL))
		  AND control.active_intent_id IS NOT NULL AND control.status<>'reconciling'
		FOR UPDATE OF control SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type expired struct{ controlID, intentID uuid.UUID }
	var values []expired
	for rows.Next() {
		var value expired
		if err = rows.Scan(&value.controlID, &value.intentID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		values = append(values, value)
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	for _, value := range values {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status='reconciling',
			last_error_code='lease_expired',last_error_message='worker lease expired',
			available_at=now(),updated_at=now() WHERE id=$1 AND source_type='github_work_item'
			AND status IN ('dispatching','awaiting_confirmation','running','reconciling')`,
			value.intentID)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status='failed',
			active_slot=NULL,error_code='lease_expired',error_message='worker lease expired',
			finished_at=COALESCE(finished_at,now()) WHERE control_id=$1 AND active_slot=1`,
			value.controlID)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status='reconciling',
			active_intent_id=NULL,lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
			last_error_code='lease_expired',last_error_message='worker lease expired',
			next_wakeup_at=now(),updated_at=now() WHERE id=$1`, value.controlID)
		if err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(values)), nil
}

func requireOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrLeaseLost
	}
	return nil
}
