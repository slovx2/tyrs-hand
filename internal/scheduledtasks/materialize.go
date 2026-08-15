package scheduledtasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const scheduledActor = "tyrs-hand-scheduler"

func (s *Service) MaterializeDueWorker(ctx context.Context, workerID uuid.UUID) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM scheduled_tasks
		WHERE id=(SELECT task.id FROM scheduled_tasks task
			JOIN worker_workspaces workspace ON workspace.id=task.workspace_id
			WHERE workspace.worker_id=$1 AND task.status='active'
			  AND task.next_run_at IS NOT NULL AND task.next_run_at<=now()
			  AND (task.blocked_until IS NULL OR task.blocked_until<=now())
			ORDER BY task.next_run_at,task.created_at
			FOR UPDATE OF task SKIP LOCKED LIMIT 1)`, workerID)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var availability string
	if err = tx.QueryRowContext(ctx, `SELECT availability_status FROM workspace_projects
		WHERE id=$1`, task.WorkspaceProjectID).Scan(&availability); err != nil {
		return false, err
	}
	if availability != "available" {
		_, err = tx.ExecContext(ctx, `UPDATE scheduled_tasks SET blocked_until=now()+interval '5 minutes',
			last_error_code='project_unavailable',last_error_message='项目当前不可用',updated_at=now()
			WHERE id=$1`, task.ID)
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}

	if active, found, activeErr := activeRunTx(ctx, tx, task.ID); activeErr != nil {
		return false, activeErr
	} else if found {
		now := s.now().UTC()
		if _, err = tx.ExecContext(ctx, `UPDATE scheduled_task_runs SET
			coalesced_through=$2,updated_at=now() WHERE id=$1`, active.ID, now); err != nil {
			return false, err
		}
		if err = s.advanceTaskTx(ctx, tx, task, now); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}

	if task.Kind == KindHeartbeat {
		blocked, blockErr := s.blockHeartbeatTx(ctx, tx, task)
		if blockErr != nil {
			return false, blockErr
		}
		if blocked {
			return true, tx.Commit()
		}
	}

	scheduledFor := *task.NextRunAt
	run, err := s.materializeTaskTx(ctx, tx, task, "scheduled",
		"scheduled:"+task.ID.String()+":"+fmt.Sprint(task.ScheduleRevision)+":"+
			scheduledFor.UTC().Format(time.RFC3339Nano), scheduledFor)
	if err != nil {
		return false, err
	}
	now := s.now().UTC()
	if now.After(scheduledFor) {
		_, err = tx.ExecContext(ctx, `UPDATE scheduled_task_runs SET coalesced_through=$2,
			updated_at=now() WHERE id=$1`, run.ID, now)
	}
	if err == nil {
		err = s.advanceTaskTx(ctx, tx, task, now)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func activeRunTx(ctx context.Context, tx *sql.Tx, taskID uuid.UUID) (Run, bool, error) {
	result, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+`
		FROM scheduled_task_runs WHERE scheduled_task_id=$1
		  AND status IN ('queued','running','waiting_for_user')
		ORDER BY created_at LIMIT 1 FOR UPDATE`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	return result, err == nil, err
}

func (s *Service) advanceTaskTx(ctx context.Context, tx *sql.Tx, task Task, now time.Time) error {
	if task.NextRunAt == nil {
		return nil
	}
	schedule, err := ParseSchedule(task.ScheduleText, task.Timezone)
	if err != nil {
		return err
	}
	next := schedule.NextAfterOccurrence(*task.NextRunAt, now)
	status := StatusActive
	var nextValue any
	if next.IsZero() {
		status = StatusCompleted
	} else {
		nextValue = next
	}
	_, err = tx.ExecContext(ctx, `UPDATE scheduled_tasks SET status=$2,next_run_at=$3,
		blocked_until=NULL,last_run_at=$4,last_error_code=NULL,last_error_message=NULL,
		updated_at=now() WHERE id=$1 AND status='active'`, task.ID, status, nextValue, now)
	return err
}

func (s *Service) blockHeartbeatTx(ctx context.Context, tx *sql.Tx, task Task) (bool, error) {
	if task.TargetSessionID == nil {
		return false, errors.New("heartbeat 缺少目标 Session")
	}
	var lifecycle string
	var lastActivity time.Time
	var busy bool
	err := tx.QueryRowContext(ctx, `SELECT session.lifecycle_state,session.last_activity_at,
		EXISTS(SELECT 1 FROM codex_turn_intents intent
			WHERE intent.session_id=session.id AND intent.status IN
			('placement_pending','queued','dispatching','awaiting_confirmation','running',
			 'waiting_for_user','reconciling','retry_wait'))
		FROM workspace_sessions session WHERE session.id=$1 FOR UPDATE`, *task.TargetSessionID).
		Scan(&lifecycle, &lastActivity, &busy)
	if errors.Is(err, sql.ErrNoRows) || lifecycle == "archived" {
		_, updateErr := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET status='paused',
			next_run_at=NULL,blocked_until=NULL,last_error_code='target_session_inactive',
			last_error_message='目标 Session 已归档或不存在',updated_at=now() WHERE id=$1`, task.ID)
		return true, updateErr
	}
	if err != nil {
		return false, err
	}
	if lifecycle != "active" {
		_, updateErr := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET
			blocked_until=now()+interval '1 minute',last_error_code=NULL,
			last_error_message=NULL,updated_at=now() WHERE id=$1`, task.ID)
		return true, updateErr
	}
	now := s.now().UTC()
	if task.ScheduleKind == "interval" && task.IntervalSeconds != nil {
		base := lastActivity
		if task.LastRunAt != nil && task.LastRunAt.After(base) {
			base = *task.LastRunAt
		}
		eligible := base.Add(time.Duration(*task.IntervalSeconds) * time.Second)
		if eligible.After(now) {
			_, err = tx.ExecContext(ctx, `UPDATE scheduled_tasks SET blocked_until=$2,
				last_error_code=NULL,last_error_message=NULL,updated_at=now() WHERE id=$1`,
				task.ID, eligible)
			return true, err
		}
	}
	if busy {
		_, err = tx.ExecContext(ctx, `UPDATE scheduled_tasks SET
			blocked_until=now()+interval '1 minute',updated_at=now() WHERE id=$1`, task.ID)
		return true, err
	}
	return false, nil
}

func scheduledInstruction(taskID, runID uuid.UUID, scheduledFor, now time.Time,
	prompt string,
) string {
	return fmt.Sprintf(`<scheduled_task>
  <task_id>%s</task_id>
  <run_id>%s</run_id>
  <scheduled_for>%s</scheduled_for>
  <current_time_iso>%s</current_time_iso>
  <instructions>
%s
  </instructions>
</scheduled_task>
`,
		taskID, runID, scheduledFor.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339),
		strings.TrimSpace(prompt))
}
