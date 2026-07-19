package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

func (s *Server) reconcileControl(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建事务失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_intents i
		SET status = 'reconciling', attempt_count = 0, available_at = now(), finished_at = NULL,
		last_error_code = NULL, last_error_message = NULL, updated_at = now()
		FROM codex_thread_controls control
		WHERE control.id = $1 AND control.status = 'error' AND i.id = control.active_intent_id`, id)
	if err != nil {
		problem(c, http.StatusInternalServerError, "重新对账 Control 失败", err)
		return
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		problem(c, http.StatusConflict, "Control 当前不可重新对账", nil)
		return
	}
	_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls SET status = 'reconciling',
		active_intent_id = NULL, worker_id = NULL, lease_token = NULL, lease_expires_at = NULL,
		next_wakeup_at = now(), updated_at = now() WHERE id = $1`, id)
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交 Control 对账失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Control 对账失败", err)
		return
	}
	if s.redis != nil {
		_ = s.redis.Publish(c.Request.Context(), codexcontrol.WakeupChannel, "reconcile").Err()
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) resetControl(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建事务失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_intents SET status = 'canceled',
		last_error_code = 'operator_reset', last_error_message = 'Control reset by operator',
		finished_at = now(), updated_at = now()
		WHERE control_id = $1 AND status NOT IN ('completed','failed','canceled')`, id)
	if err == nil {
		result, updateErr := tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls SET
			status = 'idle', external_thread_id = NULL, active_intent_id = NULL,
			active_codex_turn_id = NULL, active_client_id = NULL, remote_status = NULL,
			worker_id = NULL, lease_token = NULL, lease_expires_at = NULL,
			thread_generation = thread_generation + 1, last_error_code = NULL,
			last_error_message = NULL, updated_at = now() WHERE id = $1 AND status = 'error'`, id)
		err = updateErr
		if err == nil {
			if count, _ := result.RowsAffected(); count != 1 {
				problem(c, http.StatusConflict, "只有 error Control 可以 Reset", nil)
				return
			}
		}
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "Reset Control 失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Control Reset 失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}
