package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type clientInteractiveAnswerRequest struct {
	Answer json.RawMessage `json:"answer" binding:"required"`
}

func (s *Server) clientAnswerInteractive(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request clientInteractiveAnswerRequest
	if err = c.ShouldBindJSON(&request); err != nil || !validInteractiveAnswer(request.Answer) {
		if err == nil {
			err = errors.New("交互回答参数无效")
		}
		badRequest(c, err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交交互回答失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	var questions json.RawMessage
	var sessionID, nodeID uuid.UUID
	err = tx.QueryRowContext(c.Request.Context(), `SELECT request.status,request.questions,
		request.session_id,run.execution_node_id
		FROM codex_interactive_requests request
		JOIN codex_turn_runs run ON run.id=request.run_id
		WHERE request.id=$1 AND request.session_id IS NOT NULL FOR UPDATE OF request`, id).
		Scan(&status, &questions, &sessionID, &nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "交互请求不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取交互请求失败", err)
		return
	}
	if interactiveQuestionsSecret(questions) {
		problem(c, http.StatusForbidden, "Secret 交互只能在 Codex Desktop 回答", nil)
		return
	}
	accepted := status == "pending"
	if accepted {
		result, updateErr := tx.ExecContext(c.Request.Context(), `UPDATE codex_interactive_requests
			SET status='resolved',answer=$2,answer_surface='client',resolved_at=now(),updated_at=now()
			WHERE id=$1 AND status='pending'`, id, request.Answer)
		if updateErr != nil {
			problem(c, http.StatusInternalServerError, "保存交互回答失败", updateErr)
			return
		}
		changed, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			problem(c, http.StatusInternalServerError, "确认交互回答失败", rowsErr)
			return
		}
		accepted = changed == 1
		if accepted {
			payload, _ := json.Marshal(gin.H{"requestId": id, "status": "resolved",
				"surface": "client"})
			_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO client_updates(
				session_id,update_type,entity_id,payload)
				VALUES ($1,'interactive.resolved',$2,$3)`, sessionID, id.String(), payload)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交交互回答失败", err)
		return
	}
	state, err := s.loadInteractiveState(c.Request.Context(), id, nodeID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取交互回答结果失败", err)
		return
	}
	state.Accepted = accepted
	if state.Status == "resolved" {
		state.Ready, err = s.tryResumeInteractive(c.Request.Context(), id, nodeID)
		if err != nil {
			problem(c, http.StatusInternalServerError, "恢复交互回答调度槽失败", err)
			return
		}
	}
	s.projectInteractiveBestEffort(c.Request.Context(), id)
	c.JSON(http.StatusOK, state)
}
