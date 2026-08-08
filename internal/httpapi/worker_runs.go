package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerRunHeartbeat(c *gin.Context) {
	var request workerprotocol.RunLeaseRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID, request)
	if err == nil {
		err = codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration).
			Heartbeat(c.Request.Context(), claimed)
	}
	if err != nil {
		remoteRunError(c, "GitHub Job 续租失败", err)
		return
	}
	c.JSON(http.StatusOK, workerprotocol.RunHeartbeatResponse{
		Recovery: workerprotocol.RunRecoveryState{Recovering: claimed.Recovering,
			SubmissionID: claimed.SubmissionID, ConfirmedTurnID: claimed.ConfirmedTurnID,
			ExternalThreadID: claimed.ExternalThreadID},
	})
}

func (s *Server) workerRunEvents(c *gin.Context) {
	var request workerprotocol.EventsRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID,
		request.RunLeaseRequest)
	if err != nil {
		remoteRunError(c, "校验 GitHub Job 失败", err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "记录 GitHub Job 事件失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var lastSequence int64
	if err = tx.QueryRowContext(c.Request.Context(), `SELECT worker_event_sequence
		FROM codex_turn_runs WHERE id=$1 AND worker_id=$2 FOR UPDATE`, runID,
		worker.ID).Scan(&lastSequence); err != nil {
		problem(c, http.StatusInternalServerError, "锁定 GitHub Job 事件序列失败", err)
		return
	}
	for _, event := range request.Events {
		if event.Sequence <= 0 || event.Type == "" {
			badRequest(c, errors.New("GitHub Job 事件缺少 sequence 或 type"))
			return
		}
		if event.Sequence <= lastSequence {
			continue
		}
		if event.Sequence != lastSequence+1 {
			badRequest(c, fmt.Errorf("GitHub Job 事件序号不连续：当前 %d，收到 %d",
				lastSequence, event.Sequence))
			return
		}
		externalEventID := fmt.Sprintf("worker:%d", event.Sequence)
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO agent_events(
			control_id,intent_id,run_id,event_type,external_event_id,payload,run_event_sequence)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(run_id,external_event_id)
			WHERE run_id IS NOT NULL AND external_event_id IS NOT NULL DO NOTHING`,
			claimed.ControlID, claimed.ID, claimed.RunID, event.Type, externalEventID,
			event.Payload, event.Sequence)
		if err != nil {
			problem(c, http.StatusInternalServerError, "记录 GitHub Job 事件失败", err)
			return
		}
		if event.Type == "runtime.settings_applied" {
			if err = recordRuntimeSettingsApplied(c.Request.Context(), tx, claimed,
				event.Payload); err != nil {
				badRequest(c, err)
				return
			}
		}
		lastSequence = event.Sequence
	}
	if _, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_runs
		SET worker_event_sequence=$2 WHERE id=$1`, runID, lastSequence); err != nil {
		problem(c, http.StatusInternalServerError, "更新 GitHub Job 事件序列失败", err)
		return
	}
	if err = tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 GitHub Job 事件失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func recordRuntimeSettingsApplied(ctx context.Context, tx *sql.Tx,
	claimed *codexcontrol.ClaimedControl, payload json.RawMessage,
) error {
	var value workerprotocol.RuntimeSettingsApplied
	if err := json.Unmarshal(payload, &value); err != nil {
		return fmt.Errorf("runtime.settings_applied payload 无效: %w", err)
	}
	if value.Phase != "thread/start" && value.Phase != "thread/resume" &&
		value.Phase != "turn/start" {
		return errors.New("runtime.settings_applied phase 无效")
	}
	if value.ReasoningEffort != "" && !codexsettings.ValidReasoningEffort(value.ReasoningEffort) {
		return errors.New("runtime.settings_applied reasoningEffort 无效")
	}
	appliedTier, ok := codexsettings.AppliedServiceTier(value.ServiceTier)
	if !ok {
		return errors.New("runtime.settings_applied serviceTier 无效")
	}
	value.ServiceTier = appliedTier
	if value.CollaborationMode != "" && value.CollaborationMode != "default" &&
		value.CollaborationMode != "plan" {
		return errors.New("runtime.settings_applied collaborationMode 无效")
	}
	if value.SettingsRevision < 0 {
		return errors.New("runtime.settings_applied settingsRevision 无效")
	}
	_, err := tx.ExecContext(ctx, `UPDATE codex_turn_runs SET
		applied_model=NULLIF($2,''),applied_reasoning_effort=NULLIF($3,''),
		applied_service_tier=NULLIF($4,''),applied_collaboration_mode=NULLIF($5,''),
		applied_settings_revision=$6,settings_applied_at=now() WHERE id=$1`,
		claimed.RunID, value.Model, value.ReasoningEffort, value.ServiceTier,
		value.CollaborationMode, value.SettingsRevision)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET
		applied_model=NULLIF($2,''),applied_reasoning_effort=NULLIF($3,''),
		applied_service_tier=NULLIF($4,''),applied_collaboration_mode=NULLIF($5,''),
		applied_settings_revision=$6,settings_applied_at=now(),updated_at=now()
		WHERE id=$1 AND (applied_settings_revision IS NULL OR applied_settings_revision<=$6)`,
		claimed.ControlID, value.Model, value.ReasoningEffort, value.ServiceTier,
		value.CollaborationMode, value.SettingsRevision)
	return err
}

func (s *Server) workerRunComplete(c *gin.Context) {
	var request workerprotocol.CompleteRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	if request.IdempotencyKey == "" {
		badRequest(c, errors.New("完成请求缺少幂等键"))
		return
	}
	if finished, err := s.remoteRunAlreadyFinished(c.Request.Context(), runID, worker.ID,
		request.IdempotencyKey, "completed"); err != nil {
		remoteRunError(c, "检查 GitHub Job 完成状态失败", err)
		return
	} else if finished {
		c.Status(http.StatusNoContent)
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID,
		request.RunLeaseRequest)
	repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration)
	if err == nil {
		var satisfied bool
		satisfied, err = repository.ReplySatisfied(c.Request.Context(), claimed)
		if err == nil && !satisfied {
			err = errors.New("required_reply_missing")
		}
	}
	if err == nil {
		err = repository.Complete(c.Request.Context(), claimed, request.Result)
	}
	if err != nil {
		remoteRunError(c, "完成 GitHub Job 失败", err)
		return
	}
	_, _ = s.db.ExecContext(c.Request.Context(), `UPDATE codex_turn_runs
		SET worker_terminal_key=$2 WHERE id=$1`, runID, request.IdempotencyKey)
	c.Status(http.StatusNoContent)
}

func (s *Server) workerRunFail(c *gin.Context) {
	var request workerprotocol.FailRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	if request.IdempotencyKey == "" {
		badRequest(c, errors.New("失败请求缺少幂等键"))
		return
	}
	expectedStatus := "failed"
	if request.Code == "user_interrupt" {
		expectedStatus = "canceled"
	}
	if finished, err := s.remoteRunAlreadyFinished(c.Request.Context(), runID, worker.ID,
		request.IdempotencyKey, expectedStatus); err != nil {
		remoteRunError(c, "检查 GitHub Job 失败状态失败", err)
		return
	} else if finished {
		c.Status(http.StatusNoContent)
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID,
		request.RunLeaseRequest)
	if err == nil {
		if request.Code == "codex_non_retryable_error" && request.CodexError == nil {
			badRequest(c, errors.New("不可重试 Codex 错误缺少结构化详情"))
			return
		}
		if request.CodexError != nil {
			if request.Code != "codex_non_retryable_error" || request.CodexError.WillRetry ||
				request.CodexError.Message == "" || request.CodexError.ThreadID == "" ||
				request.CodexError.TurnID == "" {
				badRequest(c, errors.New("不可重试 Codex 错误字段不完整"))
				return
			}
			if request.Message != request.CodexError.Message {
				badRequest(c, errors.New("结构化 Codex 错误消息与终态消息不一致"))
				return
			}
			if claimed.ExternalThreadID != "" &&
				request.CodexError.ThreadID != claimed.ExternalThreadID {
				badRequest(c, errors.New("结构化 Codex 错误 Thread ID 与 Run 不匹配"))
				return
			}
			expectedTurnID := claimed.ConfirmedTurnID
			if expectedTurnID == "" {
				expectedTurnID = claimed.SubmissionID
			}
			if expectedTurnID != "" && request.CodexError.TurnID != expectedTurnID {
				badRequest(c, errors.New("结构化 Codex 错误 Turn ID 与 Run 不匹配"))
				return
			}
		}
		repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration)
		switch request.Code {
		case "user_interrupt":
			err = repository.Cancel(c.Request.Context(), claimed, request.Code, request.Message)
		case "codex_non_retryable_error":
			err = repository.FailWithCodexError(c.Request.Context(), claimed, request.Code,
				emptyMessageError(request.Message), request.CodexError)
		default:
			err = repository.Reconcile(c.Request.Context(), claimed, request.Code,
				emptyMessageError(request.Message))
		}
	}
	if err != nil {
		remoteRunError(c, "提交 GitHub Job 失败状态失败", err)
		return
	}
	_, _ = s.db.ExecContext(c.Request.Context(), `UPDATE codex_turn_runs
		SET worker_terminal_key=$2 WHERE id=$1`, runID, request.IdempotencyKey)
	c.Status(http.StatusNoContent)
}

func (s *Server) remoteRunAlreadyFinished(ctx context.Context, runID, workerID uuid.UUID,
	key, expectedStatus string,
) (bool, error) {
	var status string
	var storedKey sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT status,worker_terminal_key
		FROM codex_turn_runs WHERE id=$1 AND worker_id=$2`, runID, workerID).
		Scan(&status, &storedKey)
	if err != nil {
		return false, err
	}
	if storedKey.Valid && storedKey.String != key {
		return false, errors.New("run 已使用不同幂等键结束")
	}
	if status == expectedStatus {
		if !storedKey.Valid {
			_, err = s.db.ExecContext(ctx, `UPDATE codex_turn_runs SET worker_terminal_key=$2
				WHERE id=$1 AND worker_terminal_key IS NULL`, runID, key)
		}
		return err == nil, err
	}
	if status == "completed" || status == "failed" || status == "canceled" {
		return false, errors.New("run 已进入不同终态")
	}
	return false, nil
}

func (s *Server) workerSetThread(c *gin.Context) {
	var request workerprotocol.SetThreadRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID,
		request.RunLeaseRequest)
	if err == nil {
		err = codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration).
			SetThread(c.Request.Context(), claimed, request.ThreadID)
	}
	if err != nil {
		remoteRunError(c, "保存 GitHub Job Codex Thread 失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerRecordSubmission(c *gin.Context) {
	var request workerprotocol.SubmissionRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID,
		request.RunLeaseRequest)
	if err == nil {
		err = codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration).
			RecordSubmission(c.Request.Context(), claimed, request.SubmissionID)
	}
	if err != nil {
		remoteRunError(c, "记录 GitHub Job Codex 提交失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerConfirmTurn(c *gin.Context) {
	var request workerprotocol.ConfirmTurnRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID,
		request.RunLeaseRequest)
	if err == nil {
		err = codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration).
			ConfirmTurn(c.Request.Context(), claimed, request.TurnID)
	}
	if err != nil {
		remoteRunError(c, "确认 GitHub Job Codex Turn 失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}
