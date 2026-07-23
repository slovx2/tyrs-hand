package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
	if err != nil || request.EnvironmentID == uuid.Nil || expectedTurnID == "" ||
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
	idempotencyKey := "desktop-steer:" + request.EnvironmentID.String() + ":" + request.RequestKey
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

	node := workerNode(c)
	var controlID, conversationID, repositoryID, profileID uuid.UUID
	var nullableConversation sql.NullString
	var nextSequence int64
	var controlStatus, activeTurnID, guildID, actorUserID, actorDisplayName string
	var allowedJSON, dangerousJSON []byte
	err = tx.QueryRowContext(c.Request.Context(), `SELECT ct.id, ct.discord_conversation_id,
		ct.repository_id, ct.agent_profile_id, ct.next_sequence_no, ct.status,
		COALESCE(ct.active_codex_turn_id,''), p.allowed_tools, '[]'::jsonb,
		e.guild_id, COALESCE(e.ssh_discord_user_id, ''),
		COALESCE(NULLIF(m.display_name, ''), m.username, '')
		FROM codex_thread_controls ct JOIN agent_profiles p ON p.id = ct.agent_profile_id
		JOIN discord_development_environments e ON e.id = ct.development_environment_id
		LEFT JOIN discord_members m ON m.guild_id = e.guild_id
			AND m.discord_user_id = e.ssh_discord_user_id
		WHERE ct.external_thread_id = $1 AND ct.development_environment_id = $2
		AND ct.execution_node_id = $3 FOR UPDATE OF ct`, threadID, request.EnvironmentID,
		node.ID).Scan(&controlID, &nullableConversation, &repositoryID, &profileID, &nextSequence,
		&controlStatus, &activeTurnID, &allowedJSON, &dangerousJSON, &guildID,
		&actorUserID, &actorDisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusForbidden, "Desktop Steer 的 Thread 未绑定到当前环境", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Desktop Steer Control 失败", err)
		return
	}
	conversationID = parseOptionalUUID(nullableConversation)

	intentStatus := "completed"
	if controlStatus == "active" && activeTurnID == expectedTurnID {
		var runID uuid.UUID
		err = tx.QueryRowContext(c.Request.Context(), `SELECT id FROM codex_turn_runs
			WHERE control_id = $1 AND confirmed_codex_turn_id = $2
			AND status IN ('starting','running','waiting_for_user','reconciling')
			ORDER BY started_at DESC LIMIT 1 FOR UPDATE`, controlID, expectedTurnID).Scan(&runID)
		if err == nil {
			intentStatus = "running"
			_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_runs
				SET append_count = append_count + 1 WHERE id = $1`, runID)
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
		 input_surface, discord_conversation_id, repository_id, agent_profile_id,
		 idempotency_key, instruction, prepared_input, allowed_tools, dangerous_actions,
		 priority, actor_login, actor_permission, actor_participant_id, actor_display_name,
			 reply_policy, reply_status, status, attempt_count, confirmed_codex_turn_id,
			 confirmed_at, finished_at, result_delivery_status, result_delivered_at,
			 desktop_input_projection_key, desktop_input_projection_status)
			VALUES ($1,$2,$3,'turn_input','steer_if_active','steer','discord_conversation',
				'desktop',NULLIF($4::text,'')::uuid,$5,$6,$7,$8,$9,$10,$11,100,'codex-desktop','owner',
				NULLIF($12::text,'')::uuid,$13,'silent','skipped',$14,1,$15,now(),
				CASE WHEN $14='completed' THEN now() ELSE NULL END,
				CASE WHEN $14='completed' THEN 'delivered' ELSE 'pending' END,
				CASE WHEN $14='completed' THEN now() ELSE NULL END,$16,'pending')`,
		intentID, controlID, nextSequence, nilUUIDString(conversationID), repositoryID, profileID,
		idempotencyKey, instruction, request.Params, allowedJSON, dangerousJSON,
		nilUUIDString(actorParticipantID), actorDisplayName, intentStatus, expectedTurnID,
		projectionKey)
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls SET
			next_sequence_no = next_sequence_no + 1, updated_at = now() WHERE id = $1`, controlID)
	}
	if err == nil && conversationID != uuid.Nil {
		err = enqueueDesktopInputProjection(c.Request.Context(), tx, conversationID,
			projectionKey, actorDisplayName, instruction)
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
