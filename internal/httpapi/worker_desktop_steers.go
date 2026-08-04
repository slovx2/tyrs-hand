package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerRecordDesktopSteer(c *gin.Context) {
	var request workerprotocol.DesktopSteerRecordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	threadID, instruction, err := desktopTurnInput(request.Params)
	expectedTurnID := desktopExpectedTurnID(request.Params)
	if err != nil || request.WorkspaceID == uuid.Nil || expectedTurnID == "" ||
		!validDesktopRequestKey(request.RequestKey) {
		badRequest(c, errors.New("desktop steer 参数无效"))
		return
	}
	projectionKey := desktopInputProjectionKey(request.Params, request.RequestKey)
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "记录 Desktop Steer 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	idempotencyKey := "desktop-steer:" + request.WorkspaceID.String() + ":" + request.RequestKey
	var exists bool
	if err := tx.QueryRowContext(c.Request.Context(), `SELECT EXISTS(
		SELECT 1 FROM codex_turn_intents WHERE idempotency_key = $1)`, idempotencyKey).
		Scan(&exists); err != nil {
		problem(c, http.StatusInternalServerError, "检查 Desktop Steer 幂等状态失败", err)
		return
	}
	if exists {
		c.Status(http.StatusNoContent)
		return
	}

	worker := currentWorker(c)
	var controlID, sessionID, conversationID, profileID uuid.UUID
	var nullableConversation, projectID sql.NullString
	var nextSequence int64
	var controlStatus, lifecycleState, activeTurnID, guildID, conversationThreadID string
	var actorUserID, actorDisplayName string
	var allowedJSON, dangerousJSON []byte
	err = tx.QueryRowContext(c.Request.Context(), `SELECT ct.id, ct.session_id, ct.discord_conversation_id,
		ct.workspace_project_id::text, ct.agent_profile_id, ct.next_sequence_no, ct.status,
		session.lifecycle_state,
		COALESCE(ct.active_codex_turn_id,''), p.allowed_tools, '[]'::jsonb,
		e.guild_id, COALESCE(conversation.thread_id,''), COALESCE(e.owner_discord_user_id, ''),
		COALESCE(NULLIF(m.display_name, ''), m.username, '')
		FROM codex_thread_controls ct JOIN agent_profiles p ON p.id = ct.agent_profile_id
		JOIN workspace_sessions session ON session.id=ct.session_id
		JOIN worker_workspaces e ON e.id = ct.workspace_id
		JOIN workspace_projects project ON project.id=ct.workspace_project_id
		LEFT JOIN discord_conversations conversation ON conversation.id=ct.discord_conversation_id
		LEFT JOIN desktop_thread_requests desktop_request ON desktop_request.control_id=ct.id
		JOIN discord_forums forum
			ON forum.id=COALESCE(conversation.forum_id, desktop_request.forum_id)
		LEFT JOIN discord_members m ON m.guild_id = e.guild_id
			AND m.discord_user_id = e.owner_discord_user_id
		WHERE ct.external_thread_id = $1 AND ct.workspace_id = $2
		AND ct.worker_id = $3
		AND forum.binding_status='active'
		AND project.availability_status='available' FOR UPDATE OF ct,session`, threadID, request.WorkspaceID,
		worker.ID).Scan(&controlID, &sessionID, &nullableConversation, &projectID, &profileID, &nextSequence,
		&controlStatus, &lifecycleState, &activeTurnID, &allowedJSON, &dangerousJSON, &guildID,
		&conversationThreadID, &actorUserID, &actorDisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusForbidden, "Desktop Steer 的 Thread 未绑定到当前环境", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Desktop Steer Control 失败", err)
		return
	}
	if lifecycleState != "active" {
		problem(c, http.StatusConflict, "该 Thread 已归档或正在归档", nil)
		return
	}
	conversationID = parseOptionalUUID(nullableConversation)

	intentStatus := "completed"
	var activeRunID, primaryTurnID uuid.UUID
	if controlStatus == "active" && activeTurnID == expectedTurnID {
		err = tx.QueryRowContext(c.Request.Context(), `SELECT id,primary_intent_id FROM codex_turn_runs
			WHERE control_id = $1 AND confirmed_codex_turn_id = $2
			AND status IN ('starting','running','waiting_for_user','reconciling')
			ORDER BY started_at DESC LIMIT 1 FOR UPDATE`, controlID, expectedTurnID).
			Scan(&activeRunID, &primaryTurnID)
		if err == nil {
			intentStatus = "running"
			_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_runs
				SET append_count = append_count + 1 WHERE id = $1`, activeRunID)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			problem(c, http.StatusInternalServerError, "关联 Desktop Steer Run 失败", err)
			return
		}
	}
	var actorParticipantID uuid.UUID
	if actorUserID != "" {
		actorParticipantID = participantidentity.ID(guildID, actorUserID)
	}
	intentID := uuid.New()
	_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO codex_turn_intents
		(id, control_id, sequence_no, operation, behavior, resolved_action, source_type,
		 input_surface, session_id, discord_conversation_id, repository_id,
		 workspace_project_id, agent_profile_id,
		 idempotency_key, instruction, prepared_input, allowed_tools, dangerous_actions,
		 priority, actor_login, actor_permission, actor_participant_id, actor_display_name,
			 reply_policy, reply_status, status, attempt_count, confirmed_codex_turn_id,
			 confirmed_at, finished_at, result_delivery_status, result_delivered_at,
			 desktop_input_projection_key, desktop_input_projection_status, projection_anchor)
			VALUES ($1,$2,$3,'turn_input','steer_if_active','steer','workspace_session',
			'desktop',$4,NULLIF($5::text,'')::uuid,NULLIF($6,'')::uuid,NULLIF($7,'')::uuid,$8,$9,
			$10,$11,$12,$13,100,'codex-desktop','owner',NULLIF($14::text,'')::uuid,$15,
			'silent','skipped',$16,1,$17,now(),
				CASE WHEN $16='completed' THEN now() ELSE NULL END,
				CASE WHEN $16='completed' THEN 'delivered' ELSE 'pending' END,
				CASE WHEN $16='completed' THEN now() ELSE NULL END,$18,'pending',$18)`,
		intentID, controlID, nextSequence, sessionID, nilUUIDString(conversationID), "",
		projectID.String, profileID, idempotencyKey, instruction, request.Params, allowedJSON,
		dangerousJSON, nilUUIDString(actorParticipantID), actorDisplayName, intentStatus,
		expectedTurnID, projectionKey)
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls SET
			next_sequence_no = next_sequence_no + 1, updated_at = now() WHERE id = $1`, controlID)
	}
	if err == nil {
		conversationTurnID := intentID
		if primaryTurnID != uuid.Nil {
			conversationTurnID = primaryTurnID
		}
		err = appendDesktopSessionMessageTx(c.Request.Context(), tx, sessionID,
			"desktop:"+idempotencyKey, instruction, actorParticipantID, actorDisplayName,
			intentID, conversationTurnID)
	}
	if err == nil && conversationID != uuid.Nil {
		err = enqueueDesktopInputProjection(c.Request.Context(), tx, conversationID,
			projectionKey, actorDisplayName, instruction)
	}
	if err == nil && conversationID != uuid.Nil && activeRunID != uuid.Nil {
		err = discordintegration.ProjectConversationThinkingTx(c.Request.Context(), tx, guildID,
			conversationThreadID, conversationID, projectionKey)
	}
	if err == nil && conversationID != uuid.Nil && activeRunID != uuid.Nil {
		err = discordintegration.RegisterConversationStatusSteerTx(c.Request.Context(), tx,
			activeRunID, conversationID, guildID, projectionKey)
	}
	if err == nil && conversationID != uuid.Nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_intents SET
			desktop_input_projection_status = 'projected', updated_at = now()
			WHERE id = $1`, intentID)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "持久化 Desktop Steer 失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Desktop Steer 失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func desktopExpectedTurnID(params json.RawMessage) string {
	var value struct {
		ExpectedTurnID string `json:"expectedTurnId"`
	}
	if json.Unmarshal(params, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value.ExpectedTurnID)
}
