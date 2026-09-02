package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type interactiveParams struct {
	ThreadID         string                `json:"threadId"`
	TurnID           string                `json:"turnId"`
	ItemID           string                `json:"itemId"`
	Questions        []interactiveQuestion `json:"questions"`
	AutoResolutionMS int64                 `json:"autoResolutionMs"`
}

type interactiveQuestion struct {
	ID       string              `json:"id"`
	Header   string              `json:"header"`
	Question string              `json:"question"`
	Options  []interactiveOption `json:"options,omitempty"`
	IsSecret bool                `json:"isSecret,omitempty"`
}

type interactiveOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

func (s *Server) workerRegisterInteractive(c *gin.Context) {
	var request workerprotocol.InteractiveRegisterRequest
	runID, worker, ok := requireWorkerRun(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID)
	if err != nil {
		remoteRunError(c, "校验交互请求所属 Run 失败", err)
		return
	}
	params, secret, err := parseInteractiveParams(request.Params)
	if err != nil || request.AppServerGeneration < 1 || len(request.RequestID) == 0 {
		if err == nil {
			err = errors.New("交互请求缺少 app-server generation 或 request ID")
		}
		badRequest(c, err)
		return
	}
	if claimed.ExternalThreadID != "" && claimed.ExternalThreadID != params.ThreadID {
		problem(c, http.StatusConflict, "交互请求的 Thread 与当前 Run 不一致", nil)
		return
	}
	questions, err := json.Marshal(params.Questions)
	if err != nil {
		badRequest(c, err)
		return
	}
	deadline := sql.NullTime{}
	if params.AutoResolutionMS > 0 {
		deadline = sql.NullTime{Time: time.Now().Add(time.Duration(params.AutoResolutionMS) * time.Millisecond), Valid: true}
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "登记交互请求失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var id uuid.UUID
	var inserted bool
	err = tx.QueryRowContext(c.Request.Context(), `INSERT INTO codex_interactive_requests
		(control_id, run_id, session_id, thread_id, turn_id, item_id, app_server_generation,
		 app_server_request_id, questions, deadline_at)
		VALUES ($1,$2,NULLIF($3::text,'')::uuid,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT(thread_id, turn_id, item_id) DO NOTHING RETURNING id`,
		claimed.ControlID, runID, nilUUIDString(claimed.SessionID), params.ThreadID,
		params.TurnID, params.ItemID,
		request.AppServerGeneration, request.RequestID, questions, nullableTime(deadline)).
		Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(c.Request.Context(), `SELECT id FROM codex_interactive_requests
			WHERE thread_id=$1 AND turn_id=$2 AND item_id=$3`, params.ThreadID,
			params.TurnID, params.ItemID).Scan(&id)
	} else if err == nil {
		inserted = true
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "持久化交互请求失败", err)
		return
	}
	if inserted {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_runs
			SET status='waiting_for_user', active_slot=NULL WHERE id=$1
			AND status IN ('starting','running','waiting_for_user')`, runID)
		if err == nil {
			_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_intents
				SET status='waiting_for_user', updated_at=now() WHERE id=$1`, claimed.ID)
		}
		if err == nil && claimed.SessionID != uuid.Nil {
			payload, _ := json.Marshal(gin.H{"requestId": id, "questions": params.Questions,
				"deadlineAt": nullableTime(deadline), "secret": secret})
			_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO client_updates(
				session_id,update_type,entity_type,entity_id,entity_version,payload)
				VALUES ($1,'interactive.created','interactive',$2,1,$3)`, claimed.SessionID, id.String(), payload)
		}
		if err == nil && claimed.SessionID != uuid.Nil && !secret {
			_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO client_notification_outbox(
				administrator_id,session_id,notification_type,idempotency_key,title,body,data)
				SELECT session.created_by_administrator_id,session.id,'interactive.required',$2,
				'Tyrs Hand','任务需要你的回答',jsonb_build_object(
				'serverId',instance.id,'sessionId',session.id)
				FROM workspace_sessions session CROSS JOIN control_instances instance
				WHERE session.id=$1 AND session.created_by_administrator_id IS NOT NULL
				ON CONFLICT(idempotency_key) DO NOTHING`, claimed.SessionID,
				"interactive:"+id.String())
		}
		if err != nil {
			problem(c, http.StatusInternalServerError, "释放交互等待调度槽失败", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交交互请求失败", err)
		return
	}
	s.projectInteractiveBestEffort(c.Request.Context(), id)
	state, err := s.loadInteractiveState(c.Request.Context(), id, worker.ID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取交互请求状态失败", err)
		return
	}
	state.Secret = secret || state.Secret
	c.JSON(http.StatusOK, state)
}

func (s *Server) workerInteractiveState(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	if err := s.expireInteractive(c.Request.Context(), id, currentWorker(c).ID); err != nil {
		problem(c, http.StatusInternalServerError, "更新交互请求超时状态失败", err)
		return
	}
	state, err := s.loadInteractiveState(c.Request.Context(), id, currentWorker(c).ID)
	if err != nil {
		remoteRunError(c, "读取交互请求失败", err)
		return
	}
	if state.Status == "resolved" || state.Status == "expired" {
		state.Ready, err = s.tryResumeInteractive(c.Request.Context(), id, currentWorker(c).ID)
		if err != nil {
			problem(c, http.StatusInternalServerError, "恢复交互请求调度槽失败", err)
			return
		}
	}
	if state.Status == "expired" {
		s.projectInteractiveBestEffort(c.Request.Context(), id)
	}
	c.JSON(http.StatusOK, state)
}

func (s *Server) workerAnswerInteractive(c *gin.Context) {
	var request workerprotocol.InteractiveAnswerRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.WorkspaceID == uuid.Nil || strings.TrimSpace(request.ThreadID) == "" ||
		strings.TrimSpace(request.TurnID) == "" || strings.TrimSpace(request.ItemID) == "" ||
		(request.Surface != "desktop" && request.Surface != "discord" && request.Surface != "auto") ||
		!validInteractiveAnswer(request.Answer) {
		badRequest(c, errors.New("交互回答参数无效"))
		return
	}
	worker := currentWorker(c)
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交交互回答失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var id uuid.UUID
	var status, runStatus string
	var runFinishedAt sql.NullTime
	var questions json.RawMessage
	err = tx.QueryRowContext(c.Request.Context(), `SELECT q.id, q.status, q.questions,
		r.status, r.finished_at
		FROM codex_interactive_requests q
		JOIN codex_thread_controls ct ON ct.id=q.control_id
		JOIN codex_turn_runs r ON r.id=q.run_id
		WHERE q.thread_id=$1 AND q.turn_id=$2 AND q.item_id=$3
		AND ct.workspace_id=$4 AND ct.worker_id=$5
		FOR UPDATE OF q,r`, request.ThreadID, request.TurnID, request.ItemID,
		request.WorkspaceID, worker.ID).Scan(&id, &status, &questions, &runStatus, &runFinishedAt)
	if err != nil {
		remoteRunError(c, "交互请求不存在", err)
		return
	}
	secret := interactiveQuestionsSecret(questions)
	if secret && request.Surface != "desktop" {
		problem(c, http.StatusForbidden, "Secret 交互只能在 Codex Desktop 回答", nil)
		return
	}
	if status == "pending" && (runFinishedAt.Valid || terminalRunStatus(runStatus)) {
		problem(c, http.StatusConflict, "交互请求所属任务已结束", nil)
		return
	}
	accepted := status == "pending"
	if accepted {
		answerStatus := "resolved"
		if request.Surface == "auto" {
			answerStatus = "expired"
		}
		if secret {
			if s.secrets == nil {
				problem(c, http.StatusInternalServerError, "Secret Store 未配置", nil)
				return
			}
			key := interactiveSecretKey(id)
			if err := s.secrets.PutTx(c.Request.Context(), tx, key, request.Answer); err != nil {
				problem(c, http.StatusInternalServerError, "加密保存交互回答失败", err)
				return
			}
			_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_interactive_requests q SET
				status=$2, answer=NULL, answer_secret_id=(SELECT id FROM encrypted_secrets WHERE secret_key=$3),
				answer_surface=$4, resolved_at=now(), updated_at=now() WHERE id=$1 AND status='pending'`,
				id, answerStatus, key, request.Surface)
		} else {
			_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_interactive_requests SET
				status=$2, answer=$3, answer_surface=$4, resolved_at=now(), updated_at=now()
				WHERE id=$1 AND status='pending'`, id, answerStatus, request.Answer, request.Surface)
		}
		if err != nil {
			problem(c, http.StatusInternalServerError, "持久化交互回答失败", err)
			return
		}
		if request.Surface != "auto" {
			if err = createInteractiveSegmentTx(c.Request.Context(), tx, id); err != nil {
				problem(c, http.StatusInternalServerError, "记录交互回答过程分段失败", err)
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交交互回答失败", err)
		return
	}
	state, err := s.loadInteractiveState(c.Request.Context(), id, worker.ID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取交互回答结果失败", err)
		return
	}
	state.Accepted = accepted
	if state.Status == "resolved" || state.Status == "expired" {
		state.Ready, err = s.tryResumeInteractive(c.Request.Context(), id, worker.ID)
		if err != nil {
			problem(c, http.StatusInternalServerError, "恢复交互回答调度槽失败", err)
			return
		}
	}
	s.projectInteractiveBestEffort(c.Request.Context(), id)
	c.JSON(http.StatusOK, state)
}

func parseInteractiveParams(raw json.RawMessage) (interactiveParams, bool, error) {
	var value interactiveParams
	if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value.ThreadID) == "" ||
		strings.TrimSpace(value.TurnID) == "" || strings.TrimSpace(value.ItemID) == "" ||
		len(value.Questions) < 1 || len(value.Questions) > 3 || value.AutoResolutionMS < 0 {
		return interactiveParams{}, false, errors.New("requestUserInput 参数无效")
	}
	seen := make(map[string]bool, len(value.Questions))
	secret := false
	for _, question := range value.Questions {
		id := strings.TrimSpace(question.ID)
		if id == "" || seen[id] || strings.TrimSpace(question.Question) == "" ||
			(len(question.Options) != 0 && (len(question.Options) < 2 || len(question.Options) > 3)) {
			return interactiveParams{}, false, errors.New("requestUserInput question 无效")
		}
		seen[id] = true
		for _, option := range question.Options {
			if strings.TrimSpace(option.Label) == "" {
				return interactiveParams{}, false, errors.New("requestUserInput option 无效")
			}
		}
		secret = secret || question.IsSecret
	}
	return value, secret, nil
}

func validInteractiveAnswer(raw json.RawMessage) bool {
	var value struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value.Answers == nil {
		return false
	}
	for _, answer := range value.Answers {
		if len(answer.Answers) == 0 {
			return false
		}
		for _, item := range answer.Answers {
			if strings.TrimSpace(item) == "" {
				return false
			}
		}
	}
	return true
}

func terminalRunStatus(status string) bool {
	switch status {
	case "completed", "failed", "canceled":
		return true
	default:
		return false
	}
}

func interactiveQuestionsSecret(raw json.RawMessage) bool {
	var questions []interactiveQuestion
	if json.Unmarshal(raw, &questions) != nil {
		return false
	}
	for _, question := range questions {
		if question.IsSecret {
			return true
		}
	}
	return false
}

func (s *Server) loadInteractiveState(ctx context.Context, id, workerID uuid.UUID) (workerprotocol.InteractiveState, error) {
	var state workerprotocol.InteractiveState
	var answer json.RawMessage
	var secretID sql.NullString
	var deadline sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT q.id, q.status, q.questions,
		COALESCE(q.answer,'null'::jsonb), q.answer_secret_id::text, q.deadline_at,
		COALESCE(q.answer_surface,''), COALESCE(r.active_slot=1,false)
		FROM codex_interactive_requests q JOIN codex_turn_runs r ON r.id=q.run_id
		WHERE q.id=$1 AND r.worker_id=$2`, id, workerID).Scan(&state.ID, &state.Status,
		&state.Questions, &answer, &secretID, &deadline, &state.Surface, &state.Ready)
	if err != nil {
		return workerprotocol.InteractiveState{}, err
	}
	state.Secret = interactiveQuestionsSecret(state.Questions)
	if deadline.Valid {
		state.DeadlineAt = &deadline.Time
	}
	if secretID.Valid {
		if s.secrets == nil {
			return workerprotocol.InteractiveState{}, errors.New("secret Store 未配置")
		}
		value, err := s.secrets.Get(ctx, interactiveSecretKey(id))
		if err != nil {
			return workerprotocol.InteractiveState{}, err
		}
		state.Answer = value
	} else if string(answer) != "null" {
		state.Answer = answer
	}
	return state, nil
}

func (s *Server) expireInteractive(ctx context.Context, id, workerID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `UPDATE codex_interactive_requests q SET
		status='expired', answer='{"answers":{}}'::jsonb, answer_surface='auto',
		resolved_at=now(), updated_at=now()
		FROM codex_turn_runs r WHERE q.id=$1 AND q.run_id=r.id AND r.worker_id=$2
		AND q.status='pending' AND q.deadline_at IS NOT NULL AND q.deadline_at <= now()`, id, workerID)
	return err
}

func (s *Server) tryResumeInteractive(ctx context.Context, id, workerID uuid.UUID) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var runID, controlID, intentID uuid.UUID
	var status, runStatus string
	var activeSlot sql.NullInt64
	var runFinishedAt sql.NullTime
	var maxJobs int
	err = tx.QueryRowContext(ctx, `SELECT q.run_id, q.control_id, r.primary_intent_id,
		q.status, r.status, r.active_slot, r.finished_at, n.max_concurrent_jobs
		FROM codex_interactive_requests q
		JOIN codex_turn_runs r ON r.id=q.run_id JOIN workers n ON n.id=r.worker_id
		WHERE q.id=$1 AND r.worker_id=$2 FOR UPDATE OF q,r,n`, id, workerID).
		Scan(&runID, &controlID, &intentID, &status, &runStatus, &activeSlot,
			&runFinishedAt, &maxJobs)
	if err != nil {
		return false, err
	}
	if status != "resolved" && status != "expired" {
		return false, tx.Commit()
	}
	if runFinishedAt.Valid || terminalRunStatus(runStatus) {
		return false, tx.Commit()
	}
	if activeSlot.Valid && activeSlot.Int64 == 1 {
		return true, tx.Commit()
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs
		WHERE worker_id=$1 AND active_slot=1`, workerID).Scan(&active); err != nil {
		return false, err
	}
	if active >= maxJobs {
		return false, tx.Commit()
	}
	var resumedID uuid.UUID
	err = tx.QueryRowContext(ctx, `UPDATE codex_turn_runs SET status='running', active_slot=1
		WHERE id=$1 AND status='waiting_for_user' AND finished_at IS NULL RETURNING id`, runID).
		Scan(&resumedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET status='active',
			updated_at=now() WHERE id=$1`, controlID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status='running',
			updated_at=now() WHERE id=$1`, intentID)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func interactiveSecretKey(id uuid.UUID) string {
	return "codex-interactive-answer:" + id.String()
}

func nullableTime(value sql.NullTime) any {
	if value.Valid {
		return value.Time
	}
	return nil
}

func (s *Server) projectInteractiveBestEffort(ctx context.Context, id uuid.UUID) {
	if err := discordintegration.ProjectInteractiveRequest(ctx, s.db, id); err != nil && s.logger != nil {
		s.logger.Warn("投影 Codex 交互卡片失败", zap.String("interactive_id", id.String()),
			zap.Error(err))
	}
}
