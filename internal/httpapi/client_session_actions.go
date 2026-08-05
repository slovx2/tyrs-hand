package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
)

func (s *Server) clientStopSession(c *gin.Context) {
	sessionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	actor := c.MustGet("session").(auth.Session)
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "停止 Session 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	requestID := uuid.New()
	repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration,
		s.cfg.CodexMaxSteersPerTurn, s.cfg.CodexReconcileMaxAttempts)
	_, inserted, err := repository.Enqueue(c.Request.Context(), tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceWorkspace, SessionID: sessionID,
		InputSurface: "client", IdempotencyKey: "client:stop:" + requestID.String(),
		Operation: "interrupt", Instruction: "stopped from Tyrs Hand",
		ReplyPolicy: "silent", ActorLogin: actor.Username, ActorPermission: "owner",
		ActorParticipantID: actor.AdministratorID, ActorDisplayName: actor.Username,
	})
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Session 不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusConflict, "Session 当前无法停止", err)
		return
	}
	result, err := tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_intents SET
		status='canceled',last_error_code='user_interrupt',
		last_error_message='stopped from Tyrs Hand',finished_at=now(),updated_at=now()
		WHERE control_id=(SELECT id FROM codex_thread_controls WHERE session_id=$1)
		  AND operation='turn_input' AND status IN ('placement_pending','queued','retry_wait')`,
		sessionID)
	var count int64
	if err == nil {
		count, err = result.RowsAffected()
	}
	if err == nil && count == 0 && inserted {
		count = 1
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "停止 Session 失败", err)
		return
	}
	if s.redis != nil {
		_ = s.redis.Publish(c.Request.Context(), codexcontrol.WakeupChannel, "queued").Err()
	}
	c.JSON(http.StatusAccepted, gin.H{"accepted": inserted, "affected": count})
}

type clientLifecycleResult struct {
	ID           uuid.UUID `json:"id"`
	SessionID    uuid.UUID `json:"sessionId"`
	DesiredState string    `json:"desiredState"`
	Status       string    `json:"status"`
	Revision     int64     `json:"revision"`
}

func (s *Server) clientArchiveSession(c *gin.Context) {
	s.clientChangeSessionLifecycle(c, "archived")
}

func (s *Server) clientRestoreSession(c *gin.Context) {
	s.clientChangeSessionLifecycle(c, "active")
}

func (s *Server) clientChangeSessionLifecycle(c *gin.Context, desired string) {
	sessionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	actor := c.MustGet("session").(auth.Session)
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "修改 Session 生命周期失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var result clientLifecycleResult
	var controlID, workspaceID uuid.UUID
	var conversationID sql.NullString
	var current string
	err = tx.QueryRowContext(c.Request.Context(), `SELECT control.id,
		control.workspace_id,control.discord_conversation_id::text,
		session.lifecycle_state,control.lifecycle_revision
		FROM workspace_sessions session JOIN codex_thread_controls control
		ON control.session_id=session.id WHERE session.id=$1 FOR UPDATE OF session,control`,
		sessionID).Scan(&controlID, &workspaceID, &conversationID, &current, &result.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Session 不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Session 生命周期失败", err)
		return
	}
	result.SessionID, result.DesiredState = sessionID, desired
	if (desired == "archived" && current == "archived") ||
		(desired == "active" && current == "active") {
		result.Status = "completed"
		if err = tx.Commit(); err == nil {
			c.JSON(http.StatusOK, result)
		}
		return
	}
	err = tx.QueryRowContext(c.Request.Context(), `SELECT id,status FROM
		codex_thread_lifecycle_requests WHERE control_id=$1 AND desired_state=$2
		AND status IN ('waiting_for_turn','applying') ORDER BY created_at DESC LIMIT 1`,
		controlID, desired).Scan(&result.ID, &result.Status)
	if err == nil {
		if err = tx.Commit(); err == nil {
			c.JSON(http.StatusOK, result)
		}
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusInternalServerError, "读取生命周期请求失败", err)
		return
	}
	var active bool
	if err = tx.QueryRowContext(c.Request.Context(), `SELECT EXISTS(SELECT 1
		FROM codex_turn_runs WHERE control_id=$1 AND status IN
		('starting','running','waiting_for_user','reconciling'))`, controlID).Scan(&active); err != nil {
		problem(c, http.StatusInternalServerError, "检查 Session 运行状态失败", err)
		return
	}
	var emptyDesktop bool
	if err = tx.QueryRowContext(c.Request.Context(), `SELECT
		session.last_message_seq=0
		AND EXISTS(SELECT 1 FROM desktop_thread_requests request
			WHERE request.control_id=$2 AND request.status='waiting_for_input')
		AND NOT EXISTS(SELECT 1 FROM session_messages message
			WHERE message.session_id=session.id)
		AND NOT EXISTS(SELECT 1 FROM codex_turn_intents intent
			WHERE intent.control_id=$2 AND intent.status IN
			('placement_pending','queued','dispatching','awaiting_confirmation',
			'running','waiting_for_user','reconciling','retry_wait'))
		FROM workspace_sessions session WHERE session.id=$1`, sessionID, controlID).
		Scan(&emptyDesktop); err != nil {
		problem(c, http.StatusInternalServerError, "检查空 Desktop Session 失败", err)
		return
	}
	if emptyDesktop && !active {
		result.ID, result.Revision, result.Status = uuid.New(), result.Revision+1, "completed"
		err = s.completeEmptyDesktopLifecycle(c, tx, result, controlID, workspaceID,
			conversationID, actor.AdministratorID)
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			problem(c, http.StatusInternalServerError, "修改空 Desktop Session 生命周期失败", err)
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}
	result.ID, result.Revision, result.Status = uuid.New(), result.Revision+1, "applying"
	pendingState := "unarchive_pending"
	if desired == "archived" {
		pendingState = "archive_pending"
		if active {
			result.Status = "waiting_for_turn"
		}
	}
	_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_lifecycle_requests SET
		status='canceled',completed_at=now(),updated_at=now() WHERE control_id=$1
		AND status IN ('waiting_for_turn','applying')`, controlID)
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls SET
			lifecycle_state=$2,lifecycle_revision=$3,lifecycle_last_error=NULL,updated_at=now()
			WHERE id=$1`, controlID, pendingState, result.Revision)
	}
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE workspace_sessions SET
			lifecycle_state=$2,updated_at=now() WHERE id=$1`, sessionID, pendingState)
	}
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE discord_conversations SET
			lifecycle_state=$2,lifecycle_revision=$3,lifecycle_projection_error=NULL,
			updated_at=now() WHERE session_id=$1`, sessionID, pendingState, result.Revision)
	}
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO
			codex_thread_lifecycle_requests(id,control_id,workspace_id,source,desired_state,
			status,revision,requested_by_administrator_id)
			VALUES ($1,$2,$3,'client',$4,$5,$6,$7)`, result.ID, controlID, workspaceID,
			desired, result.Status, result.Revision, actor.AdministratorID)
	}
	if err == nil {
		_, err = insertClientUpdate(c.Request.Context(), tx, &sessionID,
			"session.lifecycle", "session", sessionID.String(), nil, &result.Revision,
			gin.H{"sessionId": sessionID, "lifecycleState": pendingState,
				"revision": result.Revision})
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "修改 Session 生命周期失败", err)
		return
	}
	c.JSON(http.StatusAccepted, result)
}

func (s *Server) completeEmptyDesktopLifecycle(c *gin.Context, tx *sql.Tx,
	result clientLifecycleResult, controlID, workspaceID uuid.UUID,
	conversationID sql.NullString, administratorID uuid.UUID,
) error {
	_, err := tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_lifecycle_requests SET
		status='canceled',completed_at=now(),updated_at=now() WHERE control_id=$1
		AND status IN ('waiting_for_turn','applying')`, controlID)
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls SET
			lifecycle_state=$2,lifecycle_revision=$3,lifecycle_last_error=NULL,updated_at=now()
			WHERE id=$1`, controlID, result.DesiredState, result.Revision)
	}
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE workspace_sessions SET
			lifecycle_state=$2,updated_at=now() WHERE id=$1`, result.SessionID,
			result.DesiredState)
	}
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO
			codex_thread_lifecycle_requests(id,control_id,workspace_id,source,desired_state,
			status,revision,requested_by_administrator_id,completed_at)
			VALUES ($1,$2,$3,'client',$4,'completed',$5,$6,now())`, result.ID, controlID,
			workspaceID, result.DesiredState, result.Revision, administratorID)
	}
	var snapshot clientSession
	if err == nil {
		snapshot, err = scanClientSession(tx.QueryRowContext(c.Request.Context(), `SELECT `+
			clientSessionColumns+` FROM workspace_sessions WHERE id=$1`, result.SessionID))
	}
	if err == nil {
		_, err = insertClientUpdate(c.Request.Context(), tx, &result.SessionID,
			"session.lifecycle", "session", result.SessionID.String(), nil, &result.Revision,
			snapshot)
	}
	if err != nil || !conversationID.Valid {
		return err
	}
	_, err = tx.ExecContext(c.Request.Context(), `UPDATE discord_conversations SET
		lifecycle_state=$2,lifecycle_revision=$3,lifecycle_projection_error=NULL,
		updated_at=now() WHERE id=$1`, conversationID.String, result.DesiredState,
		result.Revision)
	if err != nil {
		return err
	}
	parsedConversationID, err := uuid.Parse(conversationID.String)
	if err != nil {
		return err
	}
	return discordintegration.EnqueueConversationLifecycleTx(c.Request.Context(), tx,
		parsedConversationID)
}
