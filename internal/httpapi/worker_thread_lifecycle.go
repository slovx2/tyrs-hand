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
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerPrepareDesktopThreadLifecycle(c *gin.Context) {
	var request workerprotocol.ThreadLifecyclePrepareRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	request.ThreadID = strings.TrimSpace(request.ThreadID)
	if request.EnvironmentID == uuid.Nil || request.ThreadID == "" ||
		(request.DesiredState != "active" && request.DesiredState != "archived") {
		badRequest(c, errors.New("desktop Thread lifecycle 请求无效"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "登记 Thread lifecycle 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var result workerprotocol.ThreadLifecycleState
	err = tx.QueryRowContext(c.Request.Context(), `SELECT request.id, request.control_id,
		request.environment_id, control.external_thread_id, request.desired_state,
		request.status, request.revision, COALESCE(request.response,'null'::jsonb),
		COALESCE(request.error,'')
		FROM codex_thread_lifecycle_requests request
		JOIN codex_thread_controls control ON control.id = request.control_id
		JOIN discord_development_environments environment
			ON environment.id = request.environment_id
		WHERE request.environment_id = $1 AND control.external_thread_id = $2
			AND request.source = 'desktop'
			AND request.desired_state = $3
			AND request.status IN ('waiting_for_turn','applying')
			AND environment.execution_node_id = $4
		ORDER BY request.created_at DESC LIMIT 1 FOR UPDATE OF request, control`,
		request.EnvironmentID, request.ThreadID, request.DesiredState, workerNode(c).ID).
		Scan(&result.ID, &result.ControlID, &result.EnvironmentID, &result.ThreadID,
			&result.DesiredState, &result.Status, &result.Revision, &result.Response,
			&result.Error)
	if err == nil {
		if err := tx.Commit(); err != nil {
			problem(c, http.StatusInternalServerError, "提交 Thread lifecycle 失败", err)
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusInternalServerError, "读取 Thread lifecycle 失败", err)
		return
	}
	var conversationID sql.NullString
	var currentState string
	err = tx.QueryRowContext(c.Request.Context(), `SELECT control.id,
		control.discord_conversation_id::text, control.lifecycle_state,
		control.lifecycle_revision
		FROM codex_thread_controls control
		JOIN discord_development_environments environment
			ON environment.id = control.development_environment_id
		WHERE control.development_environment_id = $1
			AND control.external_thread_id = $2
			AND environment.execution_node_id = $3
		FOR UPDATE OF control`, request.EnvironmentID, request.ThreadID, workerNode(c).ID).
		Scan(&result.ControlID, &conversationID, &currentState, &result.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "当前环境没有绑定这个 Codex Thread", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Thread Control 失败", err)
		return
	}
	result.ID, result.EnvironmentID = uuid.New(), request.EnvironmentID
	result.ThreadID, result.DesiredState = request.ThreadID, request.DesiredState
	result.Revision++
	result.Status = "applying"
	pendingState := "unarchive_pending"
	if request.DesiredState == "archived" {
		pendingState, result.Status = "archive_pending", "waiting_for_turn"
	}
	_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls SET
		lifecycle_state = $2, lifecycle_revision = $3, lifecycle_last_error = NULL,
		updated_at = now() WHERE id = $1`, result.ControlID, pendingState, result.Revision)
	if err == nil && conversationID.Valid {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE discord_conversations SET
			lifecycle_state = $2, lifecycle_revision = $3,
			lifecycle_projection_error = NULL, updated_at = now() WHERE id = $1`,
			conversationID.String, pendingState, result.Revision)
	}
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO codex_thread_lifecycle_requests
			(id, control_id, environment_id, source, desired_state, status, revision)
			VALUES ($1,$2,$3,'desktop',$4,$5,$6)`, result.ID, result.ControlID,
			result.EnvironmentID, result.DesiredState, result.Status, result.Revision)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "登记 Thread lifecycle 失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Thread lifecycle 失败", err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

func (s *Server) workerPendingThreadLifecycles(c *gin.Context) {
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT request.id,
		request.control_id, request.environment_id, control.external_thread_id,
		request.desired_state, request.status, request.revision,
		COALESCE(request.response,'null'::jsonb), COALESCE(request.error,'')
		FROM codex_thread_lifecycle_requests request
		JOIN codex_thread_controls control ON control.id = request.control_id
		JOIN discord_development_environments environment
			ON environment.id = request.environment_id
		WHERE request.source = 'discord' AND request.status = 'applying'
			AND request.response IS NULL
			AND environment.execution_node_id = $1
		ORDER BY request.created_at, request.id`, workerNode(c).ID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取待执行 Thread lifecycle 失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	result := make([]workerprotocol.ThreadLifecycleState, 0)
	for rows.Next() {
		var item workerprotocol.ThreadLifecycleState
		if err := rows.Scan(&item.ID, &item.ControlID, &item.EnvironmentID, &item.ThreadID,
			&item.DesiredState, &item.Status, &item.Revision, &item.Response,
			&item.Error); err != nil {
			problem(c, http.StatusInternalServerError, "读取待执行 Thread lifecycle 失败", err)
			return
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		problem(c, http.StatusInternalServerError, "读取待执行 Thread lifecycle 失败", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) workerThreadLifecycleState(c *gin.Context) {
	requestID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Thread lifecycle 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var result workerprotocol.ThreadLifecycleState
	err = tx.QueryRowContext(c.Request.Context(), `SELECT request.id, request.control_id,
		request.environment_id, control.external_thread_id, request.desired_state,
		request.status, request.revision, COALESCE(request.response,'null'::jsonb),
		COALESCE(request.error,'')
		FROM codex_thread_lifecycle_requests request
		JOIN codex_thread_controls control ON control.id = request.control_id
		JOIN discord_development_environments environment
			ON environment.id = request.environment_id
		WHERE request.id = $1 AND environment.execution_node_id = $2
		FOR UPDATE OF request`, requestID, workerNode(c).ID).
		Scan(&result.ID, &result.ControlID, &result.EnvironmentID, &result.ThreadID,
			&result.DesiredState, &result.Status, &result.Revision, &result.Response,
			&result.Error)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Thread lifecycle 请求不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Thread lifecycle 失败", err)
		return
	}
	if result.Status == "waiting_for_turn" {
		var active bool
		err = tx.QueryRowContext(c.Request.Context(), `SELECT EXISTS(
			SELECT 1 FROM codex_turn_runs WHERE control_id = $1
				AND status IN ('starting','running','waiting_for_user','reconciling')
		)`, result.ControlID).Scan(&active)
		if err == nil && !active {
			_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_lifecycle_requests
				SET status = 'applying', updated_at = now()
				WHERE id = $1 AND status = 'waiting_for_turn'`, result.ID)
			result.Status = "applying"
		}
		if err != nil {
			problem(c, http.StatusInternalServerError, "检查 Thread Run 状态失败", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Thread lifecycle 状态失败", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) workerCompleteThreadLifecycle(c *gin.Context) {
	requestID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request workerprotocol.ThreadLifecycleCompleteRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.EnvironmentID == uuid.Nil {
		badRequest(c, errors.New("thread lifecycle complete 缺少环境"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "完成 Thread lifecycle 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var controlID uuid.UUID
	var conversationID sql.NullString
	var desiredState string
	var revision int64
	err = tx.QueryRowContext(c.Request.Context(), `SELECT request.control_id,
		control.discord_conversation_id::text, request.desired_state, request.revision
		FROM codex_thread_lifecycle_requests request
		JOIN codex_thread_controls control ON control.id = request.control_id
		JOIN discord_development_environments environment
			ON environment.id = request.environment_id
		WHERE request.id = $1 AND request.environment_id = $2
			AND environment.execution_node_id = $3
			AND request.status IN ('waiting_for_turn','applying')
		FOR UPDATE OF request, control`, requestID, request.EnvironmentID, workerNode(c).ID).
		Scan(&controlID, &conversationID, &desiredState, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		c.Status(http.StatusNoContent)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Thread lifecycle 请求失败", err)
		return
	}
	if request.Error == "" {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_lifecycle_requests SET
			status = 'applying', response = $2, error = NULL, updated_at = now()
			WHERE id = $1`, requestID, nullableLifecycleResponse(request.Response))
	} else {
		finalState := "active"
		if desiredState == "active" {
			finalState = "archived"
		}
		failure := safeDesktopFailure(request.Error)
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_lifecycle_requests SET
			status = 'failed', response = $2, error = $3,
			completed_at = now(), updated_at = now() WHERE id = $1`,
			requestID, nullableLifecycleResponse(request.Response), failure)
		if err == nil {
			_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls SET
				lifecycle_state = $2, lifecycle_last_error = $3,
				updated_at = now() WHERE id = $1 AND lifecycle_revision = $4`,
				controlID, finalState, failure, revision)
		}
		if err == nil && conversationID.Valid {
			_, err = tx.ExecContext(c.Request.Context(), `UPDATE discord_conversations SET
				lifecycle_state = $2, lifecycle_projection_error = $3,
				updated_at = now() WHERE id = $1 AND lifecycle_revision = $4`,
				conversationID.String, finalState, failure, revision)
		}
		if err == nil && conversationID.Valid {
			var parsedConversationID uuid.UUID
			parsedConversationID, err = uuid.Parse(conversationID.String)
			if err == nil {
				err = discordintegration.EnqueueConversationLifecycleTx(c.Request.Context(), tx,
					parsedConversationID)
			}
		}
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "完成 Thread lifecycle 失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Thread lifecycle 失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func nullableLifecycleResponse(value json.RawMessage) any {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}
