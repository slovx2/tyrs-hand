package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerRecordThreadMetadata(c *gin.Context) {
	var request workerprotocol.ThreadMetadataRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.EnvironmentID == uuid.Nil || request.Generation <= 0 || len(request.Events) == 0 {
		badRequest(c, errors.New("thread metadata 请求无效"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "记录 Thread metadata 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	for _, event := range request.Events {
		name := normalizeDesktopTitle(event.Name)
		if strings.TrimSpace(event.ThreadID) == "" || event.Sequence <= 0 || name == "" {
			badRequest(c, errors.New("thread metadata event 无效"))
			return
		}
		var controlID uuid.UUID
		var conversationID sql.NullString
		var revision int64
		err = tx.QueryRowContext(c.Request.Context(), `UPDATE codex_thread_controls control SET
			desired_thread_name = $4, desired_thread_name_source = 'codex',
			desired_thread_name_revision = desired_thread_name_revision + 1,
			thread_name_last_error = NULL, app_server_event_generation = $5,
			app_server_event_sequence = $6, updated_at = now()
			FROM discord_development_environments environment
			WHERE control.development_environment_id = environment.id
				AND control.external_thread_id = $3
				AND control.development_environment_id = $1
				AND environment.execution_node_id = $2
				AND ($5 > control.app_server_event_generation OR
					($5 = control.app_server_event_generation
						AND $6 > control.app_server_event_sequence))
			RETURNING control.id, control.discord_conversation_id::text,
				control.desired_thread_name_revision`, request.EnvironmentID, workerNode(c).ID,
			event.ThreadID, name, request.Generation, event.Sequence).
			Scan(&controlID, &conversationID, &revision)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			problem(c, http.StatusInternalServerError, "记录 Thread metadata 失败", err)
			return
		}
		if conversationID.Valid {
			var threadID string
			err = tx.QueryRowContext(c.Request.Context(), `UPDATE discord_conversations
				SET title = $2, generated_title = $2, updated_at = now()
				WHERE id = $1 RETURNING thread_id`, conversationID.String, name).Scan(&threadID)
			if err == nil {
				err = discordintegration.EnqueueThreadName(c.Request.Context(), tx,
					controlID, threadID, name, revision)
			}
			if err != nil {
				problem(c, http.StatusInternalServerError, "排队 Discord Thread 标题失败", err)
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Thread metadata 失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerPendingThreadNames(c *gin.Context) {
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT control.id,
		control.development_environment_id, control.external_thread_id,
		control.desired_thread_name, control.desired_thread_name_revision
		FROM codex_thread_controls control
		JOIN discord_development_environments environment
			ON environment.id = control.development_environment_id
		WHERE environment.execution_node_id = $1
			AND control.desired_thread_name_source = 'luna'
			AND control.desired_thread_name_revision > control.applied_thread_name_revision
			AND control.external_thread_id IS NOT NULL
		ORDER BY control.updated_at, control.id`, workerNode(c).ID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取待应用 Thread 标题失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	result := make([]workerprotocol.ThreadNameUpdate, 0)
	for rows.Next() {
		var item workerprotocol.ThreadNameUpdate
		if err := rows.Scan(&item.ControlID, &item.EnvironmentID, &item.ThreadID,
			&item.Name, &item.Revision); err != nil {
			problem(c, http.StatusInternalServerError, "读取待应用 Thread 标题失败", err)
			return
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		problem(c, http.StatusInternalServerError, "读取待应用 Thread 标题失败", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) workerAckThreadName(c *gin.Context) {
	controlID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request workerprotocol.ThreadNameAckRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.EnvironmentID == uuid.Nil || request.Revision <= 0 {
		badRequest(c, errors.New("thread name ack 无效"))
		return
	}
	if request.Error == "" {
		_, err = s.db.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls control SET
			applied_thread_name = desired_thread_name,
			applied_thread_name_revision = $4, thread_name_last_error = NULL, updated_at = now()
			FROM discord_development_environments environment
			WHERE control.id = $1 AND control.development_environment_id = $2
				AND environment.id = control.development_environment_id
				AND environment.execution_node_id = $3
				AND control.desired_thread_name_revision = $4`,
			controlID, request.EnvironmentID, workerNode(c).ID, request.Revision)
	} else {
		_, err = s.db.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls control SET
			thread_name_last_error = $4, updated_at = now()
			FROM discord_development_environments environment
			WHERE control.id = $1 AND control.development_environment_id = $2
				AND environment.id = control.development_environment_id
				AND environment.execution_node_id = $3
				AND control.desired_thread_name_revision = $5`,
			controlID, request.EnvironmentID, workerNode(c).ID,
			safeDesktopFailure(request.Error), request.Revision)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "确认 Thread 标题失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}
