package codexcontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// PendingWorkerInput 返回指定 Worker 最早的一条待决议输入，不创建 Run，
// 也不改变 Control 的执行状态。start/steer 由 Worker 根据本地状态决定。
func (r *Repository) PendingWorkerInput(ctx context.Context, workerID uuid.UUID) (*ClaimedControl, error) {
	var claimed ClaimedControl
	var workItemID, conversationID, sessionID, repositoryID, projectID sql.NullString
	var actorParticipantID, targetIntentID, externalThreadID sql.NullString
	var skillsJSON, toolsJSON, dangerousJSON []byte
	err := r.db.QueryRowContext(ctx, `SELECT i.id,i.control_id,i.sequence_no,i.operation,
		COALESCE(i.behavior,''),i.source_type,COALESCE(i.input_surface,''),
		i.work_item_id::text,i.discord_conversation_id::text,i.session_id::text,
		i.repository_id::text,i.workspace_project_id::text,i.agent_profile_id,
		COALESCE(i.discord_message_id,''),i.instruction,i.skills,i.allowed_tools,
		i.dangerous_actions,i.actor_login,i.actor_permission,i.actor_participant_id::text,
		i.actor_display_name,i.reply_policy,i.reply_status,i.attempt_count+1,
		GREATEST(i.max_attempts,1),COALESCE(i.codex_submission_id,''),
		COALESCE(i.confirmed_codex_turn_id,''),i.created_at,i.target_intent_id::text,
		COALESCE(i.projection_anchor,''),i.message_edit_revision,
		COALESCE(i.replacement_phase,''),c.external_thread_id,c.collaboration_mode
	FROM codex_turn_intents i
	JOIN codex_thread_controls c ON c.id=i.control_id
	LEFT JOIN workspace_sessions session ON session.id=c.session_id
	WHERE c.worker_id=$1 AND i.source_type='workspace_session'
	  AND i.status IN ('queued','retry_wait','reconciling')
	  AND i.available_at<=now() AND i.resolved_action IS NULL
	  AND c.lifecycle_state='active'
	  AND COALESCE(session.lifecycle_state,'active')='active'
	ORDER BY i.created_at,i.sequence_no LIMIT 1`, workerID).Scan(
		&claimed.ID, &claimed.ControlID, &claimed.Sequence, &claimed.Operation,
		&claimed.Behavior, &claimed.SourceType, &claimed.InputSurface,
		&workItemID, &conversationID, &sessionID, &repositoryID, &projectID,
		&claimed.AgentProfileID, &claimed.DiscordMessageID, &claimed.Instruction,
		&skillsJSON, &toolsJSON, &dangerousJSON, &claimed.ActorLogin,
		&claimed.ActorPermission, &actorParticipantID, &claimed.ActorDisplayName,
		&claimed.ReplyPolicy, &claimed.ReplyStatus, &claimed.Attempt,
		&claimed.MaxAttempts, &claimed.SubmissionID, &claimed.ConfirmedTurnID,
		&claimed.CreatedAt, &targetIntentID, &claimed.ProjectionAnchor,
		&claimed.MessageEditRevision, &claimed.ReplacementPhase,
		&externalThreadID, &claimed.CollaborationMode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 Worker 待同步输入: %w", err)
	}
	claimed.ExternalThreadID = externalThreadID.String
	claimed.MaxSteers = r.maxSteers
	if err := parseUUIDs(&claimed.Intent, workItemID.String, conversationID.String,
		sessionID.String, repositoryID.String, projectID.String); err != nil {
		return nil, err
	}
	if actorParticipantID.Valid {
		claimed.ActorParticipantID, err = uuid.Parse(actorParticipantID.String)
	}
	if err == nil && targetIntentID.Valid {
		claimed.TargetIntentID, err = uuid.Parse(targetIntentID.String)
	}
	if err == nil {
		err = json.Unmarshal(skillsJSON, &claimed.Skills)
	}
	if err == nil {
		err = json.Unmarshal(toolsJSON, &claimed.AllowedTools)
	}
	if err == nil {
		err = json.Unmarshal(dangerousJSON, &claimed.DangerousActions)
	}
	return &claimed, err
}

// StartWorkerInput 将 Worker 已经在本地接受的输入登记为活动 Run。
// 同一 input/run 可安全重放；Control 侧旧镜像不能阻止 Worker 的真实状态。
func (r *Repository) StartWorkerInput(ctx context.Context, workerID, inputID, runID uuid.UUID) error {
	if workerID == uuid.Nil || inputID == uuid.Nil || runID == uuid.Nil {
		return errors.New("Worker 输入决议缺少 ID")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var controlID, sessionID uuid.UUID
	var status string
	var attempt, maxAppend int
	err = tx.QueryRowContext(ctx, `SELECT i.control_id,i.session_id,i.status,
		i.attempt_count+1,$3 FROM codex_turn_intents i
		JOIN codex_thread_controls c ON c.id=i.control_id
		WHERE i.id=$1 AND c.worker_id=$2 AND i.source_type='workspace_session'
		FOR UPDATE OF i,c`, inputID, workerID, r.maxSteers).Scan(
		&controlID, &sessionID, &status, &attempt, &maxAppend)
	if err != nil {
		return err
	}
	var existingInput uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT primary_intent_id FROM codex_turn_runs
		WHERE id=$1 AND worker_id=$2`, runID, workerID).Scan(&existingInput)
	if err == nil {
		if existingInput != inputID {
			return errors.New("run 已绑定其他输入")
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if status != "queued" && status != "retry_wait" && status != "reconciling" {
		return errors.New("输入已由其他决议处理")
	}
	var displaced int64
	result, err := tx.ExecContext(ctx, `UPDATE codex_turn_runs SET active_slot=NULL,
		status=CASE WHEN status IN ('starting','running','waiting_for_user')
			THEN 'reconciling' ELSE status END
		WHERE control_id=$1 AND active_slot=1 AND id<>$2`, controlID, runID)
	if err == nil {
		displaced, err = result.RowsAffected()
	}
	if err != nil {
		return err
	}
	if displaced > 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(action,resource_type,resource_id,metadata)
			VALUES ('worker.run.reconciled','codex_thread_control',$1,
			jsonb_build_object('workerId',$2::text,'runId',$3::text))`,
			controlID.String(), workerID, runID)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status='dispatching',
		resolved_action='start',attempt_count=attempt_count+1,
		dispatched_at=COALESCE(dispatched_at,now()),updated_at=now() WHERE id=$1`, inputID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO codex_turn_runs(
			id,control_id,primary_intent_id,attempt,active_slot,max_append_count,worker_id,
			collaboration_mode,model,reasoning_effort,service_tier,settings_revision)
			SELECT $1,$2,$3,$4,1,$5,$6,c.collaboration_mode,c.model,c.reasoning_effort,
				c.service_tier,c.settings_revision FROM codex_thread_controls c WHERE c.id=$2`,
			runID, controlID, inputID, attempt, maxAppend, workerID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status='dispatching',
			active_intent_id=$2,lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
			updated_at=now() WHERE id=$1`, controlID, inputID)
	}
	if err == nil {
		payload := encode(map[string]any{"runId": runID,
			"conversationTurnId": inputID, "status": "starting"})
		_, err = tx.ExecContext(ctx, `INSERT INTO client_updates(
			session_id,update_type,entity_type,entity_id,payload)
			VALUES ($1,'run.started','turn',$2,$3)`, sessionID, inputID.String(), payload)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
