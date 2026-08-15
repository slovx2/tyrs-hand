package scheduledtasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

func (s *Service) RunNow(ctx context.Context, tool ToolContext, taskID uuid.UUID) (Run, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	task, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+`
		FROM scheduled_tasks task WHERE task.id=$1 AND task.workspace_id=(
			SELECT workspace_id FROM workspace_sessions WHERE id=$2) FOR UPDATE`,
		taskID, tool.SessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, errors.New("定时任务不存在或不属于当前 Workspace")
	}
	if err != nil {
		return Run{}, false, err
	}
	if task.Status == StatusDeleted {
		return Run{}, false, errors.New("已删除的定时任务不能运行")
	}
	if active, found, activeErr := activeRunTx(ctx, tx, task.ID); activeErr != nil {
		return Run{}, false, activeErr
	} else if found {
		if err = tx.Commit(); err != nil {
			return Run{}, false, err
		}
		return active, true, nil
	}
	now := s.now().UTC()
	run, err := s.materializeTaskTx(ctx, tx, task, "run_now", "run_now:"+tool.CallID, now)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE scheduled_tasks SET last_run_at=$2,
			last_error_code=NULL,last_error_message=NULL,updated_at=now() WHERE id=$1`, task.ID, now)
	}
	if err != nil {
		return Run{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Run{}, false, err
	}
	return run, false, nil
}

func (s *Service) materializeTaskTx(ctx context.Context, tx *sql.Tx, task Task,
	trigger, triggerKey string, scheduledFor time.Time,
) (Run, error) {
	runID := uuid.New()
	sessionID, runtimeSnapshot, err := s.runSessionTx(ctx, tx, task, scheduledFor)
	if err != nil {
		return Run{}, err
	}
	repository := codexcontrol.NewRepository(s.db, s.leaseDuration, s.maxSteers, s.maxAttempts)
	intentID, inserted, err := repository.Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceWorkspace, SessionID: sessionID, InputSurface: "client",
		SkipDiscordBinding: true,
		IdempotencyKey:     "scheduled:" + runID.String(), MessageLocalID: "scheduled:" + runID.String(),
		Instruction:        scheduledInstruction(task.ID, runID, scheduledFor, s.now().UTC(), task.Prompt),
		DisplayInstruction: task.Prompt, Behavior: "start_when_idle", ReplyPolicy: "silent",
		ActorLogin: scheduledActor, ActorPermission: "system", ActorDisplayName: "Tyrs Hand Scheduler",
	})
	if err != nil {
		return Run{}, err
	}
	if !inserted {
		return Run{}, errors.New("定时任务 Intent 幂等键冲突")
	}
	if task.Kind == KindStandalone {
		if err = projectStandaloneSessionTitleTx(ctx, tx, sessionID); err != nil {
			return Run{}, err
		}
	}
	snapshot, _ := json.Marshal(map[string]any{"task": task, "runtime": runtimeSnapshot})
	return scanRun(tx.QueryRowContext(ctx, `INSERT INTO scheduled_task_runs(
		id,scheduled_task_id,schedule_revision,trigger,trigger_key,scheduled_for,
		status,intent_id,session_id,task_snapshot)
		VALUES ($1,$2,$3,$4,$5,$6,'queued',$7,$8,$9)
		RETURNING `+runColumns, runID, task.ID, task.ScheduleRevision, trigger, triggerKey,
		scheduledFor, intentID, sessionID, snapshot))
}

func projectStandaloneSessionTitleTx(ctx context.Context, tx *sql.Tx, sessionID uuid.UUID) error {
	result, err := tx.ExecContext(ctx, `UPDATE codex_thread_controls control SET
		desired_thread_name=session.title,desired_thread_name_source='fallback',
		desired_thread_name_revision=control.desired_thread_name_revision+1,
		thread_name_last_error=NULL,updated_at=now()
		FROM workspace_sessions session
		WHERE control.session_id=session.id AND session.id=$1
		  AND session.title_source='manual' AND session.title<>''
		  AND control.desired_thread_name IS NULL`, sessionID)
	if err != nil {
		return fmt.Errorf("投影定时任务 Session 标题: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("确认定时任务 Session 标题投影: %w", err)
	}
	if updated != 1 {
		return errors.New("定时任务 Session 标题没有进入 Codex 投影队列")
	}
	return nil
}

type runtimeSettingsSnapshot struct {
	AgentProfileID    uuid.UUID `json:"agentProfileId"`
	Model             *string   `json:"model,omitempty"`
	ReasoningEffort   *string   `json:"reasoningEffort,omitempty"`
	ServiceTier       string    `json:"serviceTier"`
	CollaborationMode string    `json:"collaborationMode"`
}

func (s *Service) runSessionTx(ctx context.Context, tx *sql.Tx, task Task,
	scheduledFor time.Time,
) (uuid.UUID, runtimeSettingsSnapshot, error) {
	if task.Kind == KindHeartbeat {
		if task.TargetSessionID == nil {
			return uuid.Nil, runtimeSettingsSnapshot{}, errors.New("heartbeat 缺少目标 Session")
		}
		settings, lifecycle, err := scanRuntimeSettings(tx.QueryRowContext(ctx,
			`SELECT agent_profile_id,model,reasoning_effort,service_tier,collaboration_mode,
			 lifecycle_state FROM workspace_sessions WHERE id=$1 FOR UPDATE`, *task.TargetSessionID))
		if err != nil {
			return uuid.Nil, runtimeSettingsSnapshot{}, err
		}
		if lifecycle != "active" {
			return uuid.Nil, runtimeSettingsSnapshot{}, errors.New("heartbeat 目标 Session 不可写")
		}
		return *task.TargetSessionID, settings, nil
	}
	if task.AgentProfileID == nil {
		return uuid.Nil, runtimeSettingsSnapshot{}, errors.New("standalone 缺少 Agent Profile")
	}
	location, err := time.LoadLocation(task.Timezone)
	if err != nil {
		return uuid.Nil, runtimeSettingsSnapshot{}, err
	}
	title := fmt.Sprintf("定时任务 · %s · %s", task.Name,
		scheduledFor.In(location).Format("2006-01-02 15:04 MST"))
	tier := "standard"
	if task.ServiceTier != nil && *task.ServiceTier != "" {
		tier = *task.ServiceTier
	}
	var sessionID uuid.UUID
	err = tx.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,created_by_administrator_id,
		title,lifecycle_state,model,reasoning_effort,service_tier,collaboration_mode,
		settings_version,title_revision,title_source)
		VALUES ($1,$2,$3,$4,$5,'active',$6,$7,$8,'default',1,0,'manual') RETURNING id`,
		task.WorkspaceID, task.WorkspaceProjectID, *task.AgentProfileID,
		nullableUUIDValue(task.CreatedByAdministratorID), title, nullableText(task.Model),
		nullableText(task.ReasoningEffort), tier).Scan(&sessionID)
	if err != nil {
		return uuid.Nil, runtimeSettingsSnapshot{}, err
	}
	return sessionID, runtimeSettingsSnapshot{AgentProfileID: *task.AgentProfileID,
		Model: task.Model, ReasoningEffort: task.ReasoningEffort, ServiceTier: tier,
		CollaborationMode: "default"}, nil
}

func scanRuntimeSettings(row rowScanner) (runtimeSettingsSnapshot, string, error) {
	var result runtimeSettingsSnapshot
	var model, effort sql.NullString
	var lifecycle string
	err := row.Scan(&result.AgentProfileID, &model, &effort, &result.ServiceTier,
		&result.CollaborationMode, &lifecycle)
	result.Model, result.ReasoningEffort = nullableString(model), nullableString(effort)
	return result, lifecycle, err
}
