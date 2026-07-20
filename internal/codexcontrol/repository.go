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
)

type Repository struct {
	db            *sql.DB
	leaseDuration time.Duration
	maxSteers     int
	maxAttempts   int
}

func NewRepository(db *sql.DB, leaseDuration time.Duration, maxSteers ...int) *Repository {
	value := 5
	attempts := 3
	if len(maxSteers) > 0 && maxSteers[0] > 0 {
		value = maxSteers[0]
	}
	if len(maxSteers) > 1 && maxSteers[1] > 0 {
		attempts = maxSteers[1]
	}
	return &Repository{db: db, leaseDuration: leaseDuration, maxSteers: value, maxAttempts: attempts}
}

func (r *Repository) Enqueue(ctx context.Context, tx *sql.Tx, request EnqueueRequest) (uuid.UUID, bool, error) {
	if request.ReplyPolicy == "" {
		request.ReplyPolicy = "silent"
	}
	if request.Operation == "" {
		request.Operation = "turn_input"
	}
	if request.Behavior == "" && request.Operation == "turn_input" {
		request.Behavior = "steer_if_active"
	}
	var controlID uuid.UUID
	if request.SourceType == SourceGitHub {
		err := tx.QueryRowContext(ctx, `INSERT INTO codex_thread_controls
			(source_type, work_item_id, repository_id, agent_profile_id, context_version)
			VALUES ('github_work_item', $1, $2, $3, $4)
			ON CONFLICT(work_item_id, agent_profile_id, context_version) WHERE work_item_id IS NOT NULL
			DO UPDATE SET updated_at = now() RETURNING id`, request.WorkItemID, request.RepositoryID,
			request.AgentProfileID, request.ContextVersion).Scan(&controlID)
		if err != nil {
			return uuid.Nil, false, err
		}
	} else {
		err := tx.QueryRowContext(ctx, `INSERT INTO codex_thread_controls
			(source_type, discord_conversation_id, repository_id, agent_profile_id, context_version)
			VALUES ('discord_conversation', $1, NULLIF($2::text, '')::uuid, $3, $4)
			ON CONFLICT(discord_conversation_id, agent_profile_id, context_version)
				WHERE discord_conversation_id IS NOT NULL
			DO UPDATE SET repository_id = EXCLUDED.repository_id, updated_at = now() RETURNING id`,
			request.DiscordConversationID, nilUUID(request.RepositoryID), request.AgentProfileID,
			request.ContextVersion).Scan(&controlID)
		if err != nil {
			return uuid.Nil, false, err
		}
	}
	var controlStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM codex_thread_controls WHERE id = $1`, controlID).
		Scan(&controlStatus); err != nil {
		return uuid.Nil, false, err
	}
	if controlStatus == "error" {
		return uuid.Nil, false, ErrControlTerminated
	}

	var sequence int64
	if err := tx.QueryRowContext(ctx, `UPDATE codex_thread_controls
		SET next_sequence_no = next_sequence_no + 1, updated_at = now()
		WHERE id = $1 RETURNING next_sequence_no - 1`, controlID).Scan(&sequence); err != nil {
		return uuid.Nil, false, err
	}
	var intentID uuid.UUID
	err := tx.QueryRowContext(ctx, `INSERT INTO codex_turn_intents(
		control_id, sequence_no, operation, behavior, source_type, work_item_id,
		discord_conversation_id, discord_message_id, repository_id, agent_profile_id,
		webhook_delivery_id, trigger_rule_id, trigger_evidence, idempotency_key,
		instruction, skills, allowed_tools, dangerous_actions, priority,
		actor_login, actor_permission, reply_policy, reply_status)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6::text,'')::uuid,NULLIF($7::text,'')::uuid,
		NULLIF($8,''),NULLIF($9::text,'')::uuid,$10,NULLIF($11::text,'')::uuid,
		NULLIF($12::text,'')::uuid,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,
		CASE WHEN $22 = 'required' THEN 'pending' ELSE 'skipped' END)
		ON CONFLICT(idempotency_key) DO NOTHING RETURNING id`, controlID, sequence,
		request.Operation, request.Behavior, request.SourceType, nilUUID(request.WorkItemID),
		nilUUID(request.DiscordConversationID), request.DiscordMessageID, nilUUID(request.RepositoryID),
		request.AgentProfileID, nilUUID(request.WebhookDeliveryID), nilUUID(request.TriggerRuleID),
		defaultJSON(request.TriggerEvidence), request.IdempotencyKey, request.Instruction,
		encode(request.Skills), encode(request.AllowedTools), encode(request.DangerousActions),
		request.Priority, request.ActorLogin, request.ActorPermission, request.ReplyPolicy).Scan(&intentID)
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

func defaultJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func interval(value time.Duration) string { return fmt.Sprintf("%f seconds", value.Seconds()) }

func (r *Repository) Claim(ctx context.Context, workerID string) (*ClaimedControl, error) {
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
	err = tx.QueryRowContext(ctx, `SELECT c.id, c.status
		FROM codex_thread_controls c
		WHERE c.status <> 'error'
		  AND (c.lease_expires_at IS NULL OR c.lease_expires_at < now())
		  AND EXISTS (SELECT 1 FROM codex_turn_intents i
			WHERE i.control_id = c.id AND i.status IN ('queued','retry_wait','reconciling')
			  AND i.available_at <= now() AND i.attempt_count < $1)
		ORDER BY COALESCE(c.next_wakeup_at, c.created_at), c.created_at
		FOR UPDATE SKIP LOCKED LIMIT 1`, r.maxAttempts).Scan(&controlID, &oldStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("领取 Codex Control: %w", err)
	}
	var claimed ClaimedControl
	var skillsJSON, toolsJSON, dangerousJSON []byte
	var workItemID, conversationID, repositoryID, discordMessageID sql.NullString
	var externalThreadID, codexHomeKey, providerSignature sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT i.id, i.sequence_no, i.operation, COALESCE(i.behavior,''),
		i.source_type, i.work_item_id::text, i.discord_conversation_id::text,
		i.repository_id::text, i.agent_profile_id, COALESCE(i.discord_message_id,''),
		i.instruction, i.skills, i.allowed_tools, i.dangerous_actions,
		i.actor_login, i.actor_permission, i.reply_policy, i.reply_status,
		i.attempt_count + 1, $2::integer, COALESCE(i.codex_submission_id,''),
		COALESCE(i.confirmed_codex_turn_id,''), i.created_at,
		c.external_thread_id, c.codex_home_key, c.provider_signature, c.lease_epoch + 1
		FROM codex_turn_intents i JOIN codex_thread_controls c ON c.id = i.control_id
		WHERE i.control_id = $1 AND i.status IN ('queued','retry_wait','reconciling')
		  AND i.available_at <= now() AND i.attempt_count < $2
		ORDER BY i.sequence_no FOR UPDATE OF i LIMIT 1`, controlID, r.maxAttempts).Scan(
		&claimed.ID, &claimed.Sequence, &claimed.Operation, &claimed.Behavior,
		&claimed.SourceType, &workItemID, &conversationID, &repositoryID,
		&claimed.AgentProfileID, &discordMessageID, &claimed.Instruction,
		&skillsJSON, &toolsJSON, &dangerousJSON, &claimed.ActorLogin,
		&claimed.ActorPermission, &claimed.ReplyPolicy, &claimed.ReplyStatus,
		&claimed.Attempt, &claimed.MaxAttempts, &claimed.SubmissionID, &claimed.ConfirmedTurnID,
		&claimed.CreatedAt, &externalThreadID, &codexHomeKey, &providerSignature,
		&claimed.LeaseEpoch)
	if err != nil {
		return nil, err
	}
	claimed.ControlID = controlID
	claimed.Recovering = oldStatus == "reconciling" || claimed.SubmissionID != "" || claimed.ConfirmedTurnID != ""
	claimed.DiscordMessageID = discordMessageID.String
	claimed.ExternalThreadID = externalThreadID.String
	claimed.CodexHomeKey = codexHomeKey.String
	claimed.ProviderSignature = providerSignature.String
	if err := parseUUIDs(&claimed.Intent, workItemID.String, conversationID.String, repositoryID.String); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(skillsJSON, &claimed.Skills); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(toolsJSON, &claimed.AllowedTools); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(dangerousJSON, &claimed.DangerousActions); err != nil {
		return nil, err
	}
	claimed.LeaseToken = leaseToken
	claimed.LeaseExpiresAt = time.Now().Add(r.leaseDuration)
	_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status = 'dispatching',
		active_intent_id = $2, worker_id = $3, lease_token = $4, lease_epoch = $5,
		lease_expires_at = now() + $6::interval, heartbeat_at = now(), updated_at = now()
		WHERE id = $1`, controlID, claimed.ID, workerID, security.Digest(leaseToken),
		claimed.LeaseEpoch, interval(r.leaseDuration))
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'dispatching',
		attempt_count = attempt_count + 1, max_attempts = $2,
		dispatched_at = COALESCE(dispatched_at, now()), updated_at = now()
		WHERE id = $1`, claimed.ID, r.maxAttempts)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO codex_turn_runs
		(control_id, primary_intent_id, attempt, worker_id, lease_epoch, capability_hash, active_slot, max_append_count)
		VALUES ($1,$2,$3,$4,$5,$6,1,$7) RETURNING id`, controlID, claimed.ID,
		claimed.Attempt, workerID, claimed.LeaseEpoch, security.Digest(capability), r.maxSteers).Scan(&claimed.RunID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	claimed.Capability = capability
	return &claimed, nil
}

func parseUUIDs(intent *Intent, workItem, conversation, repository string) error {
	for _, item := range []struct {
		source string
		target *uuid.UUID
	}{{workItem, &intent.WorkItemID}, {conversation, &intent.DiscordConversationID}, {repository, &intent.RepositoryID}} {
		source, target := item.source, item.target
		if source == "" {
			continue
		}
		value, err := uuid.Parse(source)
		if err != nil {
			return err
		}
		*target = value
	}
	return nil
}

func (r *Repository) Heartbeat(ctx context.Context, claimed *ClaimedControl) error {
	result, err := r.db.ExecContext(ctx, `WITH updated_control AS (
		UPDATE codex_thread_controls
		SET lease_expires_at = now() + $4::interval, heartbeat_at = now(), updated_at = now()
		WHERE id = $1 AND lease_token = $2 AND lease_epoch = $3
		  AND active_intent_id = $5 AND status IN ('dispatching','active','stopping','reconciling')
		RETURNING id
	)
	UPDATE codex_turn_runs SET heartbeat_at = now()
	WHERE id = $6 AND control_id = (SELECT id FROM updated_control) AND active_slot = 1`,
		claimed.ControlID, security.Digest(claimed.LeaseToken), claimed.LeaseEpoch,
		interval(r.leaseDuration), claimed.ID, claimed.RunID)
	if err != nil {
		return err
	}
	return requireOne(result)
}

func (r *Repository) SetThread(ctx context.Context, claimed *ClaimedControl, threadID, codexHome, signature string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE codex_thread_controls SET
		external_thread_id = $4, codex_home_key = $5, provider_signature = $6,
		status = 'active', remote_status = 'idle', last_error_code = NULL,
		last_error_message = NULL, updated_at = now()
		WHERE id = $1 AND lease_token = $2 AND lease_epoch = $3`, claimed.ControlID,
		security.Digest(claimed.LeaseToken), claimed.LeaseEpoch, threadID, codexHome, signature)
	if err == nil {
		err = requireOne(result)
	}
	return err
}

func (r *Repository) RecordSubmission(ctx context.Context, claimed *ClaimedControl, submissionID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.fence(ctx, tx, claimed); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'awaiting_confirmation',
		codex_submission_id = $2, updated_at = now() WHERE id = $1`, claimed.ID, submissionID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status = 'running',
			codex_submission_id = $2, heartbeat_at = now() WHERE id = $1`, claimed.RunID, submissionID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET remote_status = 'dispatching',
			active_client_id = $2, updated_at = now() WHERE id = $1`, claimed.ControlID, claimed.ID.String())
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ConfirmTurn(ctx context.Context, claimed *ClaimedControl, turnID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.fence(ctx, tx, claimed); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'running',
		confirmed_codex_turn_id = $2, confirmed_at = COALESCE(confirmed_at, now()), updated_at = now()
		WHERE id = $1 AND (confirmed_codex_turn_id IS NULL OR confirmed_codex_turn_id = $2)`, claimed.ID, turnID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status = 'running',
			confirmed_codex_turn_id = $2, heartbeat_at = now() WHERE id = $1`, claimed.RunID, turnID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status = 'active',
			remote_status = 'active', active_codex_turn_id = $2, updated_at = now() WHERE id = $1`,
			claimed.ControlID, turnID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) Complete(ctx context.Context, claimed *ClaimedControl, result TurnResult) error {
	return r.finish(ctx, claimed, IntentCompleted, "", "", result)
}

func (r *Repository) Cancel(ctx context.Context, claimed *ClaimedControl, code, message string) error {
	return r.finish(ctx, claimed, IntentCanceled, code, message, TurnResult{})
}

func (r *Repository) Fail(ctx context.Context, claimed *ClaimedControl, code string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return r.finish(ctx, claimed, IntentFailed, code, message, TurnResult{})
}

func (r *Repository) Reconcile(ctx context.Context, claimed *ClaimedControl, code string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.fence(ctx, tx, claimed); err != nil {
		return err
	}
	terminal := claimed.Attempt >= claimed.MaxAttempts
	intentStatus, controlStatus := "retry_wait", "reconciling"
	available := "now() + interval '15 seconds'"
	if terminal {
		intentStatus, controlStatus, available = "failed", "error", "now()"
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE codex_turn_intents SET status = $2,
		last_error_code = $3, last_error_message = $4, available_at = %s,
		finished_at = CASE WHEN $2 = 'failed' THEN now() ELSE NULL END, updated_at = now() WHERE id = $1`, available),
		claimed.ID, intentStatus, code, message)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status = 'failed', active_slot = NULL,
			error_code = $2, error_message = $3, finished_at = now() WHERE id = $1`, claimed.RunID, code, message)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status = $2,
			active_intent_id = CASE WHEN $2 = 'error' THEN active_intent_id ELSE NULL END,
			worker_id = NULL, lease_token = NULL, lease_expires_at = NULL,
			last_error_code = $3, last_error_message = $4, next_wakeup_at = now() + interval '15 seconds',
			updated_at = now() WHERE id = $1`, claimed.ControlID, controlStatus, code, message)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) finish(ctx context.Context, claimed *ClaimedControl, status IntentStatus,
	code, message string, turnResult TurnResult,
) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.fence(ctx, tx, claimed); err != nil {
		return err
	}
	var resultJSON any = encode(turnResult)
	if status != IntentCompleted {
		resultJSON = nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status = $2,
		result = $3, last_error_code = NULLIF($4,''), last_error_message = NULLIF($5,''),
		finished_at = now(), result_delivery_status = CASE
			WHEN $2 = 'completed' AND source_type = 'github_work_item' THEN 'skipped'
			WHEN $2 = 'completed' THEN 'delivered' ELSE result_delivery_status END,
		result_delivered_at = CASE WHEN $2 = 'completed' THEN now() ELSE result_delivered_at END,
		result_delivery_available_at = now(), updated_at = now()
		WHERE id = $1`, claimed.ID, status, resultJSON, code, message)
	if err == nil && status == IntentCompleted {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'completed', result = $3,
			finished_at = now(), result_delivery_status = CASE WHEN source_type = 'github_work_item'
				THEN 'skipped' ELSE 'delivered' END, result_delivered_at = now(),
			result_delivery_available_at = now(), updated_at = now()
			WHERE control_id = $1 AND id <> $2 AND status = 'running'
			  AND resolved_action = 'steer' AND confirmed_codex_turn_id = $4`,
			claimed.ControlID, claimed.ID, resultJSON, turnResult.TurnID)
	}
	if err == nil {
		runStatus := string(status)
		if status == IntentCompleted {
			runStatus = "completed"
		}
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status = $2, active_slot = NULL,
			error_code = NULLIF($3,''), error_message = NULLIF($4,''), finished_at = now()
			WHERE id = $1`, claimed.RunID, runStatus, code, message)
	}
	if err == nil {
		controlStatus := "idle"
		if status == IntentFailed {
			controlStatus = "error"
		}
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status = $2,
			active_intent_id = CASE WHEN $2 = 'error' THEN active_intent_id ELSE NULL END,
			remote_status = 'idle', active_codex_turn_id = NULL, active_client_id = NULL,
			worker_id = NULL, lease_token = NULL, lease_expires_at = NULL,
			last_error_code = NULLIF($3,''), last_error_message = NULLIF($4,''), updated_at = now()
			WHERE id = $1`, claimed.ControlID, controlStatus, code, message)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ReplySatisfied(ctx context.Context, claimed *ClaimedControl) (bool, error) {
	if claimed.ReplyPolicy != "required" {
		return true, nil
	}
	var delivered bool
	err := r.db.QueryRowContext(ctx, `SELECT reply_status = 'delivered'
		FROM codex_turn_intents WHERE id = $1`, claimed.ID).Scan(&delivered)
	return delivered, err
}

func (r *Repository) fence(ctx context.Context, tx *sql.Tx, claimed *ClaimedControl) error {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM codex_thread_controls
		WHERE id = $1 AND lease_token = $2 AND lease_epoch = $3 AND active_intent_id = $4)`,
		claimed.ControlID, security.Digest(claimed.LeaseToken), claimed.LeaseEpoch, claimed.ID).Scan(&exists)
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
	rows, err := tx.QueryContext(ctx, `SELECT id, active_intent_id FROM codex_thread_controls
		WHERE lease_expires_at < now() AND active_intent_id IS NOT NULL FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type expired struct{ controlID, intentID uuid.UUID }
	var values []expired
	for rows.Next() {
		var value expired
		if err := rows.Scan(&value.controlID, &value.intentID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, value := range values {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'reconciling',
			last_error_code = 'lease_expired', last_error_message = 'worker lease expired',
			available_at = now(), updated_at = now()
			WHERE id = $1 AND status IN ('dispatching','awaiting_confirmation','running','reconciling')`, value.intentID)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status = 'failed', active_slot = NULL,
			error_code = 'lease_expired', error_message = 'worker lease expired', finished_at = now()
			WHERE control_id = $1 AND active_slot = 1`, value.controlID)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status = 'reconciling',
			active_intent_id = NULL, worker_id = NULL, lease_token = NULL, lease_expires_at = NULL,
			last_error_code = 'lease_expired', last_error_message = 'worker lease expired',
			next_wakeup_at = now(), updated_at = now() WHERE id = $1`, value.controlID)
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(values)), nil
}

func requireOne(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrLeaseLost
	}
	return nil
}
