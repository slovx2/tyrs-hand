package scheduledtasks

import (
	"database/sql"

	"github.com/google/uuid"
)

type rowScanner interface {
	Scan(...any) error
}

const taskColumns = `id,workspace_id,workspace_project_id,target_session_id::text,
	created_by_administrator_id::text,kind,name,prompt,status,schedule_text,timezone,
	schedule_kind,interval_seconds,next_run_at,blocked_until,last_run_at,
	agent_profile_id::text,model,reasoning_effort,service_tier,schedule_revision,
	last_error_code,last_error_message,created_at,updated_at`

func scanTask(row rowScanner) (Task, error) {
	var result Task
	var targetSession, administrator, agentProfile sql.NullString
	var interval sql.NullInt64
	var nextRun, blockedUntil, lastRun sql.NullTime
	var model, effort, tier, errorCode, errorMessage sql.NullString
	err := row.Scan(&result.ID, &result.WorkspaceID, &result.WorkspaceProjectID,
		&targetSession, &administrator, &result.Kind, &result.Name, &result.Prompt,
		&result.Status, &result.ScheduleText, &result.Timezone, &result.ScheduleKind,
		&interval, &nextRun, &blockedUntil, &lastRun, &agentProfile, &model, &effort,
		&tier, &result.ScheduleRevision, &errorCode, &errorMessage, &result.CreatedAt,
		&result.UpdatedAt)
	if err != nil {
		return Task{}, err
	}
	result.TargetSessionID, err = nullableUUID(targetSession)
	if err != nil {
		return Task{}, err
	}
	result.CreatedByAdministratorID, err = nullableUUID(administrator)
	if err != nil {
		return Task{}, err
	}
	result.AgentProfileID, err = nullableUUID(agentProfile)
	if err != nil {
		return Task{}, err
	}
	if interval.Valid {
		result.IntervalSeconds = &interval.Int64
	}
	if nextRun.Valid {
		result.NextRunAt = &nextRun.Time
	}
	if blockedUntil.Valid {
		result.BlockedUntil = &blockedUntil.Time
	}
	if lastRun.Valid {
		result.LastRunAt = &lastRun.Time
	}
	result.Model = nullableString(model)
	result.ReasoningEffort = nullableString(effort)
	result.ServiceTier = nullableString(tier)
	result.LastErrorCode = nullableString(errorCode)
	result.LastErrorMessage = nullableString(errorMessage)
	return result, nil
}

const runColumns = `id,scheduled_task_id,schedule_revision,trigger,trigger_key,
	scheduled_for,coalesced_through,status,intent_id::text,session_id::text,
	task_snapshot,error_code,error_message,started_at,finished_at,created_at,updated_at`

func scanRun(row rowScanner) (Run, error) {
	var result Run
	var coalesced, started, finished sql.NullTime
	var intent, session, errorCode, errorMessage sql.NullString
	err := row.Scan(&result.ID, &result.ScheduledTaskID, &result.ScheduleRevision,
		&result.Trigger, &result.TriggerKey, &result.ScheduledFor, &coalesced,
		&result.Status, &intent, &session, &result.TaskSnapshot, &errorCode,
		&errorMessage, &started, &finished, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return Run{}, err
	}
	result.IntentID, err = nullableUUID(intent)
	if err != nil {
		return Run{}, err
	}
	result.SessionID, err = nullableUUID(session)
	if err != nil {
		return Run{}, err
	}
	if coalesced.Valid {
		result.CoalescedThrough = &coalesced.Time
	}
	if started.Valid {
		result.StartedAt = &started.Time
	}
	if finished.Valid {
		result.FinishedAt = &finished.Time
	}
	result.ErrorCode = nullableString(errorCode)
	result.ErrorMessage = nullableString(errorMessage)
	return result, nil
}

func nullableUUID(value sql.NullString) (*uuid.UUID, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := uuid.Parse(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func nullableText(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}

func nullableUUIDValue(value *uuid.UUID) any {
	if value == nil || *value == uuid.Nil {
		return nil
	}
	return *value
}
