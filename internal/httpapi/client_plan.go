package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

func (s *Server) clientExecutePlan(c *gin.Context) {
	sessionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	runID, err := uuid.Parse(c.Param("runId"))
	if err != nil {
		badRequest(c, err)
		return
	}
	actor := c.MustGet("session").(auth.Session)
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "执行 Plan 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var controlID uuid.UUID
	var status, mode, lifecycle, planContent, planOutputType string
	var started time.Time
	var finished sql.NullTime
	var settingsVersion int64
	err = tx.QueryRowContext(c.Request.Context(), `SELECT run.control_id,run.status,
		run.collaboration_mode,session.lifecycle_state,run.started_at,run.finished_at,
		session.settings_version,COALESCE(plan_intent.result->>'finalAnswer',''),
		COALESCE(plan_intent.result->>'finalOutputType','')
		FROM codex_turn_runs run
		JOIN codex_thread_controls control ON control.id=run.control_id
		JOIN codex_turn_intents plan_intent ON plan_intent.id=run.primary_intent_id
		JOIN workspace_sessions session ON session.id=control.session_id
		WHERE run.id=$1 AND session.id=$2 FOR UPDATE OF run,control,session`, runID, sessionID).
		Scan(&controlID, &status, &mode, &lifecycle, &started, &finished, &settingsVersion,
			&planContent, &planOutputType)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Plan 不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Plan 失败", err)
		return
	}
	if status != "completed" || mode != "plan" || !finished.Valid || planContent == "" ||
		planOutputType != "plan" {
		problem(c, http.StatusConflict, "Plan 尚未完成，不能执行", nil)
		return
	}
	if lifecycle != "active" {
		problem(c, http.StatusConflict, "Session 当前不可写", codexcontrol.ErrControlArchived)
		return
	}
	idempotencyKey := "client:plan:" + sessionID.String() + ":" + runID.String()
	var existing uuid.UUID
	err = tx.QueryRowContext(c.Request.Context(), `SELECT id FROM codex_turn_intents
		WHERE idempotency_key=$1`, idempotencyKey).Scan(&existing)
	if err == nil {
		if err = tx.Commit(); err == nil {
			c.JSON(http.StatusOK, gin.H{"intentId": existing, "deduplicated": true})
		}
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusInternalServerError, "检查 Plan 幂等键失败", err)
		return
	}
	var busy, stale bool
	err = tx.QueryRowContext(c.Request.Context(), `SELECT
		EXISTS(SELECT 1 FROM codex_turn_intents WHERE control_id=$1 AND status IN
		('placement_pending','queued','dispatching','awaiting_confirmation','running',
		'waiting_for_user','reconciling','retry_wait')),
		EXISTS(SELECT 1 FROM codex_turn_runs WHERE control_id=$1 AND id<>$2
		AND started_at>$3)`, controlID, runID, started).Scan(&busy, &stale)
	if err != nil {
		problem(c, http.StatusInternalServerError, "检查 Plan 状态失败", err)
		return
	}
	if busy {
		problem(c, http.StatusConflict, "当前会话仍在处理中", nil)
		return
	}
	if stale {
		problem(c, http.StatusConflict, "这个 Plan 已不是最新 Plan", nil)
		return
	}
	settingsVersion++
	_, err = tx.ExecContext(c.Request.Context(), `UPDATE workspace_sessions SET
		collaboration_mode='default',settings_version=$2,updated_at=now() WHERE id=$1`,
		sessionID, settingsVersion)
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls SET
			collaboration_mode='default',settings_revision=$2,
			runtime_preferences_frozen_at=now(),updated_at=now() WHERE id=$1`,
			controlID, settingsVersion)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "切换执行模式失败", err)
		return
	}
	if err = upsertClientParticipant(c, tx, actor); err != nil {
		problem(c, http.StatusInternalServerError, "保存参与者失败", err)
		return
	}
	repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration,
		s.cfg.CodexMaxSteersPerTurn, s.cfg.CodexReconcileMaxAttempts)
	intentID, inserted, err := repository.Enqueue(c.Request.Context(), tx,
		codexcontrol.EnqueueRequest{SourceType: codexcontrol.SourceWorkspace,
			SessionID: sessionID, InputSurface: "client", IdempotencyKey: idempotencyKey,
			MessageLocalID:     "plan-execution:" + runID.String(),
			Instruction:        codexcontrol.PlanExecutionInstruction(planContent),
			DisplayInstruction: codexcontrol.PlanExecutionDisplayText,
			Behavior:           "start_when_idle",
			ReplyPolicy:        "silent", ActorLogin: actor.Username, ActorPermission: "owner",
			ActorParticipantID: actor.AdministratorID, ActorDisplayName: actor.Username})
	if err != nil || !inserted {
		if err == nil {
			err = errors.New("Plan Intent 幂等键冲突")
		}
		problem(c, http.StatusInternalServerError, "执行 Plan 失败", err)
		return
	}
	_, err = insertClientUpdate(c.Request.Context(), tx, &sessionID, "plan.executed",
		"run", runID.String(), nil, &settingsVersion,
		gin.H{"sessionId": sessionID, "runId": runID, "intentId": intentID})
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交 Plan 失败", err)
		return
	}
	if s.redis != nil {
		_ = s.redis.Publish(c.Request.Context(), codexcontrol.WakeupChannel, "queued").Err()
	}
	c.JSON(http.StatusCreated, gin.H{"intentId": intentID, "deduplicated": false})
}
