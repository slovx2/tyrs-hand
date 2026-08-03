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
	ErrInvalidSource     = errors.New("不支持的 Codex Control 来源")
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
	var executionNodeID sql.NullString
	var developmentEnvironmentID sql.NullString
	switch request.SourceType {
	case SourceGitHub:
		if err := tx.QueryRowContext(ctx, `SELECT execution_node_id::text FROM work_items
			WHERE id = $1 FOR UPDATE`, request.WorkItemID).Scan(&executionNodeID); err != nil {
			return uuid.Nil, false, err
		}
		if !executionNodeID.Valid {
			_ = tx.QueryRowContext(ctx, `SELECT value->>'nodeId' FROM platform_settings
				WHERE setting_key = 'execution.default.github'`).Scan(&executionNodeID)
			if executionNodeID.Valid {
				if _, err := tx.ExecContext(ctx, `UPDATE work_items SET execution_node_id = $2,
					updated_at = now() WHERE id = $1 AND execution_node_id IS NULL`,
					request.WorkItemID, executionNodeID.String); err != nil {
					return uuid.Nil, false, err
				}
			}
		}
		err := tx.QueryRowContext(ctx, `INSERT INTO codex_thread_controls
			(source_type, work_item_id, repository_id, agent_profile_id, execution_node_id)
			VALUES ('github_work_item', $1, $2, $3, NULLIF($4,'')::uuid)
			ON CONFLICT(work_item_id, agent_profile_id) WHERE work_item_id IS NOT NULL
			DO UPDATE SET execution_node_id = COALESCE(codex_thread_controls.execution_node_id,
				EXCLUDED.execution_node_id), updated_at = now() RETURNING id`, request.WorkItemID,
			request.RepositoryID, request.AgentProfileID, executionNodeID.String).Scan(&controlID)
		if err != nil {
			return uuid.Nil, false, err
		}
	case SourceDevelopment:
		sessionID, err := r.lockDevelopmentSession(ctx, tx, request.SessionID,
			request.DiscordConversationID)
		if err != nil {
			return uuid.Nil, false, err
		}
		request.SessionID = sessionID
		if request.DiscordConversationID == uuid.Nil {
			_ = tx.QueryRowContext(ctx, `SELECT id FROM discord_conversations
				WHERE session_id=$1`, sessionID).Scan(&request.DiscordConversationID)
		}
		if request.DiscordConversationID != uuid.Nil {
			result, updateErr := tx.ExecContext(ctx, `UPDATE development_sessions session SET
				title=conversation.title, lifecycle_state=conversation.lifecycle_state,
				model=conversation.model, reasoning_effort=conversation.reasoning_effort,
					service_tier=COALESCE(conversation.service_tier,'standard'),
				collaboration_mode=conversation.collaboration_mode,
				settings_version=conversation.settings_revision,
				last_activity_at=GREATEST(session.last_activity_at,conversation.last_activity_at),
				updated_at=now()
				FROM discord_conversations conversation
				WHERE session.id=$1 AND conversation.id=$2
				  AND conversation.session_id=session.id`, sessionID, request.DiscordConversationID)
			if updateErr != nil {
				return uuid.Nil, false, updateErr
			}
			if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
				if rowsErr != nil {
					return uuid.Nil, false, rowsErr
				}
				return uuid.Nil, false, sql.ErrNoRows
			}
		}
		if err := tx.QueryRowContext(ctx, `SELECT environment.execution_node_id::text,
			session.development_environment_id::text, session.development_project_id,
			session.agent_profile_id FROM development_sessions session
			JOIN discord_development_environments environment
				ON environment.id=session.development_environment_id
			WHERE session.id=$1`, sessionID).Scan(&executionNodeID, &developmentEnvironmentID,
			&request.ProjectID, &request.AgentProfileID); err != nil {
			return uuid.Nil, false, err
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO codex_thread_controls
			(source_type, session_id, discord_conversation_id, development_project_id,
			 agent_profile_id, execution_node_id, development_environment_id)
			VALUES ('development_session',$1,NULLIF($2::text,'')::uuid,$3,$4,
			 NULLIF($5,'')::uuid,$6)
			ON CONFLICT(session_id) WHERE session_id IS NOT NULL DO UPDATE SET
				discord_conversation_id=COALESCE(codex_thread_controls.discord_conversation_id,
					EXCLUDED.discord_conversation_id),
				execution_node_id=COALESCE(codex_thread_controls.execution_node_id,
					EXCLUDED.execution_node_id), updated_at=now()
			RETURNING id`, sessionID, nilUUID(request.DiscordConversationID), request.ProjectID,
			request.AgentProfileID, executionNodeID.String, developmentEnvironmentID.String).
			Scan(&controlID)
		if err != nil {
			return uuid.Nil, false, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls control SET
			model=session.model, reasoning_effort=session.reasoning_effort,
			service_tier=session.service_tier, collaboration_mode=session.collaboration_mode,
			settings_revision=session.settings_version,
			runtime_preferences_frozen_at=now(), updated_at=now()
			FROM development_sessions session
			WHERE control.id=$1 AND session.id=$2 AND control.session_id=session.id`,
			controlID, sessionID)
		if err != nil {
			return uuid.Nil, false, err
		}
		if request.DiscordConversationID != uuid.Nil {
			_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls control SET
				desired_thread_name=conversation.generated_title,
				desired_thread_name_source='luna', desired_thread_name_revision=1,
				thread_name_last_error=NULL, updated_at=now()
				FROM discord_conversations conversation
				WHERE control.id=$1 AND conversation.id=$2
				  AND conversation.generated_title IS NOT NULL
				  AND control.desired_thread_name IS NULL`, controlID,
				request.DiscordConversationID)
			if err != nil {
				return uuid.Nil, false, err
			}
		}
	default:
		return uuid.Nil, false, ErrInvalidSource
	}
	var controlStatus, lifecycleState string
	if err := tx.QueryRowContext(ctx, `SELECT control.status,
		COALESCE(session.lifecycle_state,control.lifecycle_state)
		FROM codex_thread_controls control
		LEFT JOIN development_sessions session ON session.id=control.session_id
		WHERE control.id = $1`, controlID).
		Scan(&controlStatus, &lifecycleState); err != nil {
		return uuid.Nil, false, err
	}
	if controlStatus == "error" {
		if request.SourceType != SourceDevelopment || request.InputSurface != "client" ||
			request.Operation != "turn_input" {
			return uuid.Nil, false, ErrControlTerminated
		}
		result, resetErr := tx.ExecContext(ctx, `UPDATE codex_thread_controls SET
			status='idle',active_intent_id=NULL,active_codex_turn_id=NULL,active_client_id=NULL,
			remote_status=NULL,worker_id=NULL,lease_token=NULL,lease_expires_at=NULL,
			last_error_code=NULL,last_error_message=NULL,next_wakeup_at=now(),updated_at=now()
			WHERE id=$1 AND status='error'`, controlID)
		if resetErr != nil {
			return uuid.Nil, false, resetErr
		}
		changed, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return uuid.Nil, false, rowsErr
		}
		if changed != 1 {
			return uuid.Nil, false, ErrControlTerminated
		}
	}
	if lifecycleState != "active" {
		return uuid.Nil, false, ErrControlArchived
	}

	var sequence int64
	if err := tx.QueryRowContext(ctx, `UPDATE codex_thread_controls
		SET next_sequence_no = next_sequence_no + 1, updated_at = now()
		WHERE id = $1 RETURNING next_sequence_no - 1`, controlID).Scan(&sequence); err != nil {
		return uuid.Nil, false, err
	}
	var intentID uuid.UUID
	initialStatus := "queued"
	if !executionNodeID.Valid || executionNodeID.String == "" {
		initialStatus = "placement_pending"
	}
	inputSurface := request.InputSurface
	if request.SourceType == SourceDevelopment && inputSurface == "" {
		inputSurface = "client"
		if request.DiscordConversationID != uuid.Nil {
			inputSurface = "discord"
		}
	}
	err := tx.QueryRowContext(ctx, `INSERT INTO codex_turn_intents(
		control_id, sequence_no, operation, behavior, source_type, work_item_id,
		discord_conversation_id, session_id, discord_message_id, repository_id, development_project_id,
		agent_profile_id,
		webhook_delivery_id, trigger_rule_id, trigger_evidence, idempotency_key,
		instruction, skills, allowed_tools, dangerous_actions, priority,
		actor_login, actor_permission, actor_participant_id, actor_display_name,
		reply_policy, reply_status, status, input_surface, target_intent_id,
		projection_anchor, message_edit_revision, replacement_phase)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6::text,'')::uuid,NULLIF($7::text,'')::uuid,
		NULLIF($8::text,'')::uuid,NULLIF($9,''),NULLIF($10::text,'')::uuid,
		NULLIF($11::text,'')::uuid,$12,
		NULLIF($13::text,'')::uuid,NULLIF($14::text,'')::uuid,$15,$16,$17,$18,$19,$20,$21,$22,$23,
		NULLIF($24::text,'')::uuid,$25,$26,
		CASE WHEN $26 = 'required' THEN 'pending' ELSE 'skipped' END,
		$27, NULLIF($28,''), NULLIF($29::text,'')::uuid, NULLIF($30,''), $31,
		NULLIF($32,''))
		ON CONFLICT(idempotency_key) DO NOTHING RETURNING id`, controlID, sequence,
		request.Operation, request.Behavior, request.SourceType, nilUUID(request.WorkItemID),
		nilUUID(request.DiscordConversationID), nilUUID(request.SessionID), request.DiscordMessageID,
		nilUUID(request.RepositoryID), nilUUID(request.ProjectID), request.AgentProfileID,
		nilUUID(request.WebhookDeliveryID), nilUUID(request.TriggerRuleID),
		defaultJSON(request.TriggerEvidence), request.IdempotencyKey, request.Instruction,
		encode(request.Skills), encode(request.AllowedTools), encode(request.DangerousActions),
		request.Priority, request.ActorLogin, request.ActorPermission,
		nilUUID(request.ActorParticipantID), request.ActorDisplayName, request.ReplyPolicy,
		initialStatus, inputSurface, nilUUID(request.TargetIntentID), request.ProjectionAnchor,
		request.MessageEditRevision, request.ReplacementPhase).Scan(&intentID)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err == nil && request.SourceType == SourceDevelopment &&
		request.Operation == "turn_input" && request.MessageLocalID != "" {
		err = r.appendSessionInputTx(ctx, tx, intentID, request)
	}
	return intentID, err == nil, err
}

func (r *Repository) appendSessionInputTx(ctx context.Context, tx *sql.Tx, intentID uuid.UUID,
	request EnqueueRequest,
) error {
	if request.ActorParticipantID != uuid.Nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO participants(id,kind,display_name)
			VALUES ($1,'discord',$2) ON CONFLICT(id) DO UPDATE SET
			display_name=EXCLUDED.display_name,updated_at=now()`, request.ActorParticipantID,
			request.ActorDisplayName); err != nil {
			return err
		}
	}
	content := encode(map[string]any{"t": "plain", "v": map[string]any{
		"role": "user", "content": map[string]any{"type": "codex", "data": map[string]any{
			"type": "message", "message": request.Instruction,
		}},
	}})
	var messageID uuid.UUID
	var sequence int64
	var createdAt time.Time
	err := tx.QueryRowContext(ctx, `WITH sequence AS (
		UPDATE development_sessions SET last_message_seq=last_message_seq+1,
			last_activity_at=now(),updated_at=now() WHERE id=$1 RETURNING last_message_seq)
		INSERT INTO session_messages(session_id,seq,local_id,participant_id,message_role,content,
			turn_intent_id)
		SELECT $1,last_message_seq,$2,NULLIF($3::text,'')::uuid,'user',$4,$5 FROM sequence
		RETURNING id,seq,created_at`, request.SessionID, request.MessageLocalID,
		nilUUID(request.ActorParticipantID), content, intentID).Scan(&messageID, &sequence, &createdAt)
	if err != nil {
		return err
	}
	payload := encode(map[string]any{
		"messageId": messageID, "sessionId": request.SessionID, "seq": sequence,
		"localId": request.MessageLocalID, "participantId": request.ActorParticipantID,
		"role": "user", "content": map[string]any{"type": "text", "text": request.Instruction},
		"attachments": []any{}, "createdAt": createdAt, "updatedAt": createdAt,
	})
	_, err = tx.ExecContext(ctx, `INSERT INTO client_updates(
		session_id,update_type,entity_type,entity_id,entity_seq,entity_version,payload)
		VALUES ($1,'message.created','message',$2,$3,$3,$4)`, request.SessionID,
		messageID.String(), sequence, payload)
	return err
}

func (r *Repository) lockDevelopmentSession(ctx context.Context, tx *sql.Tx, sessionID,
	conversationID uuid.UUID,
) (uuid.UUID, error) {
	if sessionID != uuid.Nil {
		var lockedID uuid.UUID
		if err := tx.QueryRowContext(ctx, `SELECT id FROM development_sessions
			WHERE id=$1 FOR UPDATE`, sessionID).Scan(&lockedID); err != nil {
			return uuid.Nil, err
		}
		return lockedID, nil
	}
	if conversationID == uuid.Nil {
		return uuid.Nil, errors.New("development session 或 Discord conversation 至少需要一个")
	}
	var existing sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT session_id::text FROM discord_conversations
		WHERE id=$1 FOR UPDATE`, conversationID).Scan(&existing); err != nil {
		return uuid.Nil, err
	}
	if existing.Valid {
		return uuid.Parse(existing.String)
	}
	var created uuid.UUID
	err := tx.QueryRowContext(ctx, `INSERT INTO development_sessions(
		development_environment_id,development_project_id,agent_profile_id,title,
		lifecycle_state,model,reasoning_effort,service_tier,collaboration_mode,
		settings_version,last_activity_at,created_at,updated_at)
		SELECT forum.development_environment_id,conversation.development_project_id,
			conversation.agent_profile_id,COALESCE(conversation.generated_title,conversation.title),
			conversation.lifecycle_state,
				conversation.model,conversation.reasoning_effort,
				COALESCE(conversation.service_tier,'standard'),
			conversation.collaboration_mode,conversation.settings_revision,
			conversation.last_activity_at,conversation.created_at,conversation.updated_at
		FROM discord_conversations conversation
		JOIN discord_forums forum ON forum.id=conversation.forum_id
		WHERE conversation.id=$1 AND conversation.development_project_id IS NOT NULL
		RETURNING id`, conversationID).Scan(&created)
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE discord_conversations SET session_id=$2,
		updated_at=now() WHERE id=$1`, conversationID, created); err != nil {
		return uuid.Nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO session_surface_bindings(
		session_id,surface_type,external_key,metadata)
		SELECT $2,'discord',guild_id || ':' || thread_id,
			jsonb_build_object('conversationId',id,'guildId',guild_id,'threadId',thread_id)
		FROM discord_conversations WHERE id=$1`, conversationID, created)
	return created, err
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

func (r *Repository) Claim(ctx context.Context, workerID string) (*ClaimedControl, error) {
	return r.ClaimSource(ctx, workerID, "")
}

func (r *Repository) ClaimSource(ctx context.Context, workerID, sourceType string) (*ClaimedControl, error) {
	return r.claimSource(ctx, workerID, sourceType, "")
}

func (r *Repository) ClaimNode(ctx context.Context, workerID, sourceType string,
	executionNodeID uuid.UUID,
) (*ClaimedControl, error) {
	return r.claimSource(ctx, workerID, sourceType, executionNodeID.String())
}

func (r *Repository) claimSource(ctx context.Context, workerID, sourceType,
	executionNodeID string,
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
	err = tx.QueryRowContext(ctx, `SELECT c.id, c.status
		FROM codex_thread_controls c
		WHERE c.status <> 'error'
		  AND c.lifecycle_state = 'active'
		  AND (c.development_project_id IS NULL OR EXISTS (
			SELECT 1 FROM development_projects project
			WHERE project.id=c.development_project_id
			  AND project.availability_status='available'))
		  AND ($2 <> 'development_session' OR c.discord_conversation_id IS NULL OR EXISTS (
			SELECT 1 FROM discord_conversations conversation
			JOIN discord_forums forum ON forum.id=conversation.forum_id
			WHERE conversation.id=c.discord_conversation_id
			  AND forum.binding_status='active'))
		  AND ($3 = '' OR c.execution_node_id = $3::uuid)
			  AND ($2 <> 'development_session' OR c.discord_conversation_id IS NULL OR NOT EXISTS (
				SELECT 1 FROM discord_conversations dc
				JOIN discord_forums df ON df.id = dc.forum_id
				JOIN discord_development_operations operation
					ON operation.environment_id = df.development_environment_id
				WHERE dc.id = c.discord_conversation_id
				AND operation.status IN ('pending','running')))
		  AND (c.lease_expires_at IS NULL OR c.lease_expires_at < now())
		  AND EXISTS (SELECT 1 FROM codex_turn_intents i
			WHERE i.control_id = c.id AND i.status IN ('queued','retry_wait','reconciling')
			  AND i.available_at <= now() AND i.attempt_count < $1
			  AND ($2 = '' OR i.source_type = $2))
		ORDER BY COALESCE(c.next_wakeup_at, c.created_at), c.created_at
		FOR UPDATE SKIP LOCKED LIMIT 1`, r.maxAttempts, sourceType,
		executionNodeID).Scan(&controlID, &oldStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("领取 Codex Control: %w", err)
	}
	var claimed ClaimedControl
	var skillsJSON, toolsJSON, dangerousJSON []byte
	var workItemID, conversationID, sessionID, repositoryID, projectID sql.NullString
	var discordMessageID, actorParticipantID sql.NullString
	var targetIntentID sql.NullString
	var externalThreadID, codexHomeKey, runModel, runEffort, runTier sql.NullString
	var settingsRevision int64
	err = tx.QueryRowContext(ctx, `SELECT i.id, i.sequence_no, i.operation, COALESCE(i.behavior,''),
		i.source_type, COALESCE(i.input_surface,''), i.work_item_id::text,
		i.discord_conversation_id::text, i.session_id::text,
		i.repository_id::text, i.development_project_id::text, i.agent_profile_id,
		COALESCE(i.discord_message_id,''),
		i.instruction, i.skills, i.allowed_tools, i.dangerous_actions,
		i.actor_login, i.actor_permission, i.actor_participant_id::text,
		i.actor_display_name, i.reply_policy, i.reply_status,
		i.attempt_count + 1, $2::integer, COALESCE(i.codex_submission_id,''),
		COALESCE(i.confirmed_codex_turn_id,''), i.created_at,
		i.target_intent_id::text, COALESCE(i.projection_anchor,''), i.message_edit_revision,
		COALESCE(i.replacement_phase,''),
		c.external_thread_id, c.codex_home_key, c.lease_epoch + 1,
		c.collaboration_mode, c.model, c.reasoning_effort, c.service_tier, c.settings_revision
		FROM codex_turn_intents i JOIN codex_thread_controls c ON c.id = i.control_id
		WHERE i.control_id = $1 AND i.status IN ('queued','retry_wait','reconciling')
		  AND i.available_at <= now() AND i.attempt_count < $2
		  AND ($3 = '' OR i.source_type = $3)
		ORDER BY i.sequence_no FOR UPDATE OF i LIMIT 1`, controlID, r.maxAttempts, sourceType).Scan(
		&claimed.ID, &claimed.Sequence, &claimed.Operation, &claimed.Behavior,
		&claimed.SourceType, &claimed.InputSurface, &workItemID, &conversationID, &sessionID,
		&repositoryID, &projectID,
		&claimed.AgentProfileID, &discordMessageID, &claimed.Instruction,
		&skillsJSON, &toolsJSON, &dangerousJSON, &claimed.ActorLogin,
		&claimed.ActorPermission, &actorParticipantID, &claimed.ActorDisplayName,
		&claimed.ReplyPolicy, &claimed.ReplyStatus,
		&claimed.Attempt, &claimed.MaxAttempts, &claimed.SubmissionID, &claimed.ConfirmedTurnID,
		&claimed.CreatedAt, &targetIntentID, &claimed.ProjectionAnchor,
		&claimed.MessageEditRevision, &claimed.ReplacementPhase,
		&externalThreadID, &codexHomeKey, &claimed.LeaseEpoch,
		&claimed.CollaborationMode, &runModel, &runEffort, &runTier, &settingsRevision)
	if err != nil {
		return nil, err
	}
	claimed.ControlID = controlID
	claimed.Recovering = oldStatus == "reconciling" || claimed.SubmissionID != "" || claimed.ConfirmedTurnID != ""
	claimed.DiscordMessageID = discordMessageID.String
	claimed.ExternalThreadID = externalThreadID.String
	claimed.CodexHomeKey = codexHomeKey.String
	if targetIntentID.Valid {
		claimed.TargetIntentID, err = uuid.Parse(targetIntentID.String)
		if err != nil {
			return nil, err
		}
	}
	if actorParticipantID.Valid {
		claimed.ActorParticipantID, err = uuid.Parse(actorParticipantID.String)
		if err != nil {
			return nil, err
		}
	}
	if err := parseUUIDs(&claimed.Intent, workItemID.String, conversationID.String,
		sessionID.String, repositoryID.String, projectID.String); err != nil {
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
		(control_id, primary_intent_id, attempt, worker_id, lease_epoch, capability_hash,
		 active_slot, max_append_count, execution_node_id, collaboration_mode,
		 model, reasoning_effort, service_tier, settings_revision)
		VALUES ($1,$2,$3,$4,$5,$6,1,$7,NULLIF($8,'')::uuid,$9,$10,$11,$12,$13)
		RETURNING id`, controlID, claimed.ID,
		claimed.Attempt, workerID, claimed.LeaseEpoch, security.Digest(capability), r.maxSteers,
		executionNodeID, claimed.CollaborationMode, runModel, runEffort, runTier,
		settingsRevision).Scan(&claimed.RunID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	claimed.Capability = capability
	return &claimed, nil
}

func parseUUIDs(intent *Intent, workItem, conversation, session, repository, project string) error {
	for _, item := range []struct {
		source string
		target *uuid.UUID
	}{{workItem, &intent.WorkItemID}, {conversation, &intent.DiscordConversationID},
		{session, &intent.SessionID},
		{repository, &intent.RepositoryID}, {project, &intent.ProjectID}} {
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
		SET lease_expires_at = now() + $4::interval, heartbeat_at = now(),
			status = CASE WHEN status = 'reconciling' THEN 'active' ELSE status END,
			last_error_code = CASE WHEN status = 'reconciling' THEN NULL ELSE last_error_code END,
			last_error_message = CASE WHEN status = 'reconciling' THEN NULL ELSE last_error_message END,
			updated_at = now()
		WHERE id = $1 AND lease_token = $2 AND lease_epoch = $3
		  AND active_intent_id = $5 AND status IN ('dispatching','active','stopping','reconciling')
		RETURNING id
	)
	UPDATE codex_turn_runs SET heartbeat_at = now()
	WHERE id = $6 AND control_id = (SELECT id FROM updated_control)
	  AND (active_slot = 1 OR status = 'waiting_for_user')`,
		claimed.ControlID, security.Digest(claimed.LeaseToken), claimed.LeaseEpoch,
		interval(r.leaseDuration), claimed.ID, claimed.RunID)
	if err != nil {
		return err
	}
	return requireOne(result)
}

func (r *Repository) SetThread(ctx context.Context, claimed *ClaimedControl, threadID, codexHome string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE codex_thread_controls SET
		external_thread_id = $4, codex_home_key = $5,
		status = 'active', remote_status = 'idle', last_error_code = NULL,
		last_error_message = NULL, updated_at = now()
		WHERE id = $1 AND lease_token = $2 AND lease_epoch = $3`, claimed.ControlID,
		security.Digest(claimed.LeaseToken), claimed.LeaseEpoch, threadID, codexHome)
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
		codex_submission_id = $2,
		replacement_phase = CASE WHEN operation='replace_last_turn' THEN 'running'
			ELSE replacement_phase END, updated_at = now() WHERE id = $1`, claimed.ID, submissionID)
	if err == nil && claimed.Operation == "replace_last_turn" &&
		claimed.DiscordConversationID != uuid.Nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO discord_turn_contributors
			(run_id,conversation_id,external_turn_id,discord_user_id,first_message_id,
			 github_binding_id,github_user_id,github_login,binding_version)
			SELECT DISTINCT ON (source.discord_user_id) $1,$2,$3,source.discord_user_id,
				source.first_message_id,source.github_binding_id,source.github_user_id,
				source.github_login,source.binding_version FROM (
				SELECT message.discord_user_id,message.message_id AS first_message_id,
					message.github_binding_id,message.github_user_id,message.github_login,
					message.binding_version,message.received_at
				FROM discord_input_messages message WHERE message.turn_intent_id=$4
				UNION ALL
				SELECT contributor.discord_user_id,contributor.first_message_id,
					contributor.github_binding_id,contributor.github_user_id,
					contributor.github_login,contributor.binding_version,
					'-infinity'::timestamptz
				FROM discord_turn_contributors contributor
				JOIN codex_turn_runs old_run ON old_run.id=contributor.run_id
				WHERE old_run.primary_intent_id=$5
			) source ORDER BY source.discord_user_id,source.received_at,source.first_message_id
			ON CONFLICT(run_id,discord_user_id) DO NOTHING`, claimed.RunID,
			claimed.DiscordConversationID, submissionID, claimed.ID, claimed.TargetIntentID)
	}
	if err == nil && claimed.Operation == "replace_last_turn" {
		_, err = tx.ExecContext(ctx, `UPDATE discord_input_messages
			SET replacement_previous_intent_id=NULL WHERE turn_intent_id=$1`, claimed.ID)
	}
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
	return r.failWithCodexError(ctx, claimed, code, cause, nil)
}

func (r *Repository) FailWithCodexError(ctx context.Context, claimed *ClaimedControl,
	code string, cause error, codexError any,
) error {
	return r.failWithCodexError(ctx, claimed, code, cause, encodeOptional(codexError))
}

func (r *Repository) failWithCodexError(ctx context.Context, claimed *ClaimedControl,
	code string, cause error, codexError []byte,
) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return r.finishWithCodexError(ctx, claimed, IntentFailed, code, message, TurnResult{}, codexError)
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
	terminal := claimed.InputSurface == "desktop" || claimed.Attempt >= claimed.MaxAttempts
	intentStatus, controlStatus := "retry_wait", "reconciling"
	available := "now() + interval '15 seconds'"
	if terminal {
		intentStatus, controlStatus, available = "failed", "idle", "now()"
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE codex_turn_intents SET status = $2,
		last_error_code = $3, last_error_message = $4, available_at = %s,
		replacement_phase = CASE WHEN $2 = 'failed' AND operation='replace_last_turn'
			THEN 'terminal' ELSE replacement_phase END,
		finished_at = CASE WHEN $2 = 'failed' THEN now() ELSE NULL END, updated_at = now() WHERE id = $1`, available),
		claimed.ID, intentStatus, code, message)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'failed',
			last_error_code = NULLIF($3,''), last_error_message = NULLIF($4,''),
			finished_at = now(), result_delivery_available_at = now(), updated_at = now()
			WHERE control_id = $1 AND id <> $2 AND status = 'running'
			  AND resolved_action = 'steer' AND confirmed_codex_turn_id = $5`,
			claimed.ControlID, claimed.ID, code, message, claimed.ConfirmedTurnID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status = 'failed', active_slot = NULL,
			error_code = $2, error_message = $3, finished_at = now() WHERE id = $1`, claimed.RunID, code, message)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status = $2,
			active_intent_id = CASE WHEN $2 = 'reconciling' THEN active_intent_id ELSE NULL END,
			remote_status = CASE WHEN $2 = 'idle' THEN 'idle' ELSE remote_status END,
			active_codex_turn_id = CASE WHEN $2 = 'idle' THEN NULL ELSE active_codex_turn_id END,
			active_client_id = CASE WHEN $2 = 'idle' THEN NULL ELSE active_client_id END,
			worker_id = NULL, lease_token = NULL, lease_expires_at = NULL,
			last_error_code = $3, last_error_message = $4,
			next_wakeup_at = CASE WHEN $2 = 'reconciling'
				THEN now() + interval '15 seconds' ELSE NULL END,
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
	return r.finishWithCodexError(ctx, claimed, status, code, message, turnResult, nil)
}

func (r *Repository) finishWithCodexError(ctx context.Context, claimed *ClaimedControl, status IntentStatus,
	code, message string, turnResult TurnResult, codexError []byte,
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
		replacement_phase = CASE WHEN operation='replace_last_turn' THEN 'terminal'
			ELSE replacement_phase END,
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
	if err == nil && status != IntentCompleted {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status = $4,
			last_error_code = NULLIF($5,''), last_error_message = NULLIF($6,''),
			finished_at = now(), result_delivery_available_at = now(), updated_at = now()
			WHERE control_id = $1 AND id <> $2 AND status = 'running'
			  AND resolved_action = 'steer' AND confirmed_codex_turn_id = $3`,
			claimed.ControlID, claimed.ID, claimed.ConfirmedTurnID, status, code, message)
	}
	if err == nil && claimed.SourceType == SourceDevelopment && claimed.SessionID != uuid.Nil {
		err = r.appendSessionTerminalTx(ctx, tx, claimed, status, code, message, turnResult)
	}
	if err == nil {
		runStatus := string(status)
		if status == IntentCompleted {
			runStatus = "completed"
		}
		if codexError == nil {
			_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status = $2, active_slot = NULL,
				error_code = NULLIF($3,''), error_message = NULLIF($4,''), finished_at = now()
				WHERE id = $1`, claimed.RunID, runStatus, code, message)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status = $2, active_slot = NULL,
				error_code = NULLIF($3,''), error_message = NULLIF($4,''), codex_error = $5,
				finished_at = now() WHERE id = $1`, claimed.RunID, runStatus, code, message, codexError)
		}
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

func (r *Repository) appendSessionTerminalTx(ctx context.Context, tx *sql.Tx,
	claimed *ClaimedControl, status IntentStatus, code, message string, result TurnResult,
) error {
	payload := map[string]any{
		"id": claimed.RunID, "sessionId": claimed.SessionID, "intentId": claimed.ID,
		"status": status, "errorCode": code, "errorMessage": message,
	}
	if status == IntentCompleted {
		payload["result"] = result
	}
	payloadJSON := encode(payload)
	if _, err := tx.ExecContext(ctx, `INSERT INTO client_updates(
		session_id,update_type,entity_type,entity_id,payload)
		VALUES ($1,$2,'run',$3,$4)`, claimed.SessionID, "run."+string(status),
		claimed.RunID.String(), payloadJSON); err != nil {
		return err
	}
	if status == IntentCompleted || status == IntentFailed {
		notificationType, body := "run.completed", "任务已完成"
		if status == IntentFailed {
			notificationType, body = "run.failed", "任务执行失败"
		}
		_, notificationErr := tx.ExecContext(ctx, `INSERT INTO client_notification_outbox(
			administrator_id,session_id,notification_type,idempotency_key,title,body,data)
			SELECT session.created_by_administrator_id,session.id,$2,$3,'Tyrs Hand',$4,
			jsonb_build_object('serverId',instance.id,'sessionId',session.id)
			FROM development_sessions session CROSS JOIN control_instances instance
			WHERE session.id=$1 AND session.created_by_administrator_id IS NOT NULL
			ON CONFLICT(idempotency_key) DO NOTHING`, claimed.SessionID, notificationType,
			"intent:"+claimed.ID.String()+":"+notificationType, body)
		if notificationErr != nil {
			return notificationErr
		}
	}
	if status != IntentCompleted || result.FinalAnswer == "" {
		return nil
	}
	content := encode(map[string]any{"t": "plain", "v": map[string]any{
		"role": "agent", "content": map[string]any{"type": "codex", "data": map[string]any{
			"type": "message", "message": result.FinalAnswer,
		}},
	}})
	var messageID uuid.UUID
	var sequence int64
	var createdAt time.Time
	err := tx.QueryRowContext(ctx, `WITH sequence AS (
		UPDATE development_sessions SET last_message_seq=last_message_seq+1,
			last_activity_at=now(),updated_at=now() WHERE id=$1 RETURNING last_message_seq)
		INSERT INTO session_messages(session_id,seq,local_id,message_role,content)
		SELECT $1,last_message_seq,$2,'agent',$3 FROM sequence
		RETURNING id,seq,created_at`, claimed.SessionID, "intent-result:"+claimed.ID.String(), content).
		Scan(&messageID, &sequence, &createdAt)
	if err != nil {
		return err
	}
	messagePayload := encode(map[string]any{
		"messageId": messageID, "sessionId": claimed.SessionID, "seq": sequence,
		"localId": "intent-result:" + claimed.ID.String(), "role": "agent",
		"content":     map[string]any{"type": "text", "text": result.FinalAnswer},
		"attachments": []any{}, "createdAt": createdAt, "updatedAt": createdAt,
	})
	_, err = tx.ExecContext(ctx, `INSERT INTO client_updates(
		session_id,update_type,entity_type,entity_id,entity_seq,entity_version,payload)
		VALUES ($1,'message.created','message',$2,$3,$3,$4)`, claimed.SessionID,
		messageID.String(), sequence, messagePayload)
	return err
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
	rows, err := tx.QueryContext(ctx, `SELECT control.id, control.active_intent_id,
			control.execution_node_id::text, COALESCE(intent.input_surface, '')
		FROM codex_thread_controls AS control
		JOIN codex_turn_intents AS intent ON intent.id = control.active_intent_id
		WHERE lease_expires_at < now() AND active_intent_id IS NOT NULL
		AND control.status <> 'reconciling' FOR UPDATE OF control SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type expired struct {
		controlID, intentID uuid.UUID
		executionNodeID     sql.NullString
		inputSurface        string
	}
	var values []expired
	for rows.Next() {
		var value expired
		if err := rows.Scan(&value.controlID, &value.intentID, &value.executionNodeID,
			&value.inputSurface); err != nil {
			_ = rows.Close()
			return 0, err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, value := range values {
		if value.inputSurface == "desktop" {
			_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'failed',
				last_error_code = 'lease_expired', last_error_message = 'desktop relay lease expired',
				available_at = now(), finished_at = now(), updated_at = now()
				WHERE id = $1 AND status IN (
					'dispatching','awaiting_confirmation','running','reconciling'
				)`, value.intentID)
			if err != nil {
				return 0, err
			}
			_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET status = 'failed',
				active_slot = NULL, error_code = 'lease_expired',
				error_message = 'desktop relay lease expired', finished_at = now()
				WHERE control_id = $1 AND active_slot = 1`, value.controlID)
			if err != nil {
				return 0, err
			}
			_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status = 'idle',
				active_intent_id = NULL, remote_status = 'idle',
				active_codex_turn_id = NULL, active_client_id = NULL,
				worker_id = NULL, lease_token = NULL, lease_expires_at = NULL,
				last_error_code = 'lease_expired',
				last_error_message = 'desktop relay lease expired',
				next_wakeup_at = NULL, updated_at = now() WHERE id = $1`, value.controlID)
			if err != nil {
				return 0, err
			}
			continue
		}
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'reconciling',
			last_error_code = 'lease_expired', last_error_message = 'worker lease expired',
			available_at = now(), updated_at = now()
			WHERE id = $1 AND status IN ('dispatching','awaiting_confirmation','running','reconciling')`, value.intentID)
		if err != nil {
			return 0, err
		}
		if value.executionNodeID.Valid {
			_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status = 'reconciling',
				last_error_code = 'lease_expired', last_error_message = 'worker lease expired',
				next_wakeup_at = now(), updated_at = now() WHERE id = $1`, value.controlID)
			if err != nil {
				return 0, err
			}
			continue
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
