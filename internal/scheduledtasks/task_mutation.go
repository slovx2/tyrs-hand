package scheduledtasks

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
)

func (s *Service) taskForSession(ctx context.Context, taskID, sessionID uuid.UUID,
	lock bool,
) (Task, error) {
	query := `SELECT ` + taskColumns + ` FROM scheduled_tasks task
		WHERE task.id=$1 AND task.workspace_id=(
			SELECT workspace_id FROM workspace_sessions WHERE id=$2)`
	if lock {
		query += ` FOR UPDATE`
	}
	result, err := scanTask(s.db.QueryRowContext(ctx, query, taskID, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, errors.New("定时任务不存在或不属于当前 Workspace")
	}
	return result, err
}

func (s *Service) Update(ctx context.Context, tool ToolContext, args ToolArguments) (Task, error) {
	if args.TaskID == nil {
		return Task{}, errors.New("update 缺少 task_id")
	}
	if strings.TrimSpace(args.Kind) != "" {
		return Task{}, errors.New("kind 和目标创建后不可修改；请删除后重建任务")
	}
	task, err := s.taskForSession(ctx, *args.TaskID, tool.SessionID, false)
	if err != nil {
		return Task{}, err
	}
	if task.Status == StatusDeleted {
		return Task{}, errors.New("已删除的定时任务不能更新")
	}
	if args.Name != nil {
		task.Name = strings.TrimSpace(*args.Name)
	}
	if args.Prompt != nil {
		task.Prompt = strings.TrimSpace(*args.Prompt)
	}
	if err := validateTaskText(task.Name, task.Prompt); err != nil {
		return Task{}, err
	}

	scheduleChanged := args.Schedule != nil || args.Timezone != nil
	parsed, err := ParseSchedule(task.ScheduleText, task.Timezone)
	if err != nil {
		return Task{}, err
	}
	if scheduleChanged {
		raw, zone := task.ScheduleText, task.Timezone
		if args.Schedule != nil {
			if len(*args.Schedule) > 65536 {
				return Task{}, errors.New("schedule 过长")
			}
			raw = *args.Schedule
			zone = ""
		}
		if args.Timezone != nil {
			zone = strings.TrimSpace(*args.Timezone)
		}
		parsed, err = ParseSchedule(raw, zone)
		if err != nil {
			return Task{}, err
		}
		task.ScheduleText, task.Timezone, task.ScheduleKind = parsed.Text,
			parsed.Timezone, parsed.Kind
		task.IntervalSeconds = nil
		if parsed.Interval > 0 {
			seconds := int64(parsed.Interval / time.Second)
			task.IntervalSeconds = &seconds
		}
	}

	if args.Settings != nil {
		if task.Kind != KindStandalone {
			return Task{}, errors.New("heartbeat 不能指定 settings")
		}
		if err := validateSettings(*args.Settings); err != nil {
			return Task{}, err
		}
		if args.Settings.AgentProfileID != nil {
			if err := s.requireAgentProfile(ctx, *args.Settings.AgentProfileID); err != nil {
				return Task{}, err
			}
			task.AgentProfileID = args.Settings.AgentProfileID
		}
		task.Model = normalizedOptional(args.Settings.Model, task.Model)
		task.ReasoningEffort = normalizedOptional(args.Settings.ReasoningEffort,
			task.ReasoningEffort)
		if args.Settings.ServiceTier != nil {
			canonical, _ := codexsettings.CanonicalServiceTier(*args.Settings.ServiceTier)
			task.ServiceTier = &canonical
		}
	}

	requestedStatus := ""
	if args.Status != nil {
		requestedStatus = strings.TrimSpace(*args.Status)
		if requestedStatus != StatusActive && requestedStatus != StatusPaused {
			return Task{}, errors.New("status 只能更新为 active 或 paused")
		}
		task.Status = requestedStatus
	}
	now := s.now().UTC()
	if task.Status == StatusPaused {
		task.NextRunAt = nil
	} else if requestedStatus == StatusActive || (scheduleChanged && task.Status == StatusActive) {
		next := parsed.FirstAfter(now)
		if next.IsZero() {
			return Task{}, errors.New("日程在当前时间之后没有 occurrence，不能激活")
		}
		task.NextRunAt = &next
	}

	interval := any(nil)
	if task.IntervalSeconds != nil {
		interval = *task.IntervalSeconds
	}
	row := s.db.QueryRowContext(ctx, `UPDATE scheduled_tasks SET
		name=$3,prompt=$4,status=$5,schedule_text=$6,timezone=$7,schedule_kind=$8,
		interval_seconds=$9,next_run_at=$10,blocked_until=NULL,agent_profile_id=$11,
		model=$12,reasoning_effort=$13,service_tier=$14,
		schedule_revision=schedule_revision+1,last_error_code=NULL,last_error_message=NULL,
		updated_at=now()
		WHERE id=$1 AND workspace_id=(SELECT workspace_id FROM workspace_sessions WHERE id=$2)
		  AND status<>'deleted'
		RETURNING `+taskColumns, task.ID, tool.SessionID, task.Name, task.Prompt, task.Status,
		task.ScheduleText, task.Timezone, task.ScheduleKind, interval, task.NextRunAt,
		nullableUUIDValue(task.AgentProfileID), nullableText(task.Model),
		nullableText(task.ReasoningEffort), nullableText(task.ServiceTier))
	updated, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, errors.New("定时任务更新期间被删除或越过 Workspace 边界")
	}
	return updated, err
}

func (s *Service) Delete(ctx context.Context, tool ToolContext, taskID uuid.UUID) (Task, error) {
	row := s.db.QueryRowContext(ctx, `UPDATE scheduled_tasks SET status='deleted',
		next_run_at=NULL,blocked_until=NULL,deleted_at=now(),schedule_revision=schedule_revision+1,
		updated_at=now()
		WHERE id=$1 AND workspace_id=(SELECT workspace_id FROM workspace_sessions WHERE id=$2)
		  AND status<>'deleted' RETURNING `+taskColumns, taskID, tool.SessionID)
	deleted, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, errors.New("定时任务不存在、已删除或不属于当前 Workspace")
	}
	return deleted, err
}

func (s *Service) List(ctx context.Context, tool ToolContext, includeDeleted bool) ([]Task, error) {
	query := `SELECT ` + taskColumns + ` FROM scheduled_tasks task
		WHERE task.workspace_id=(SELECT workspace_id FROM workspace_sessions WHERE id=$1)`
	if !includeDeleted {
		query += ` AND task.status<>'deleted'`
	}
	query += ` ORDER BY task.created_at,task.id`
	rows, err := s.db.QueryContext(ctx, query, tool.SessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]Task, 0)
	for rows.Next() {
		item, scanErr := scanTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
