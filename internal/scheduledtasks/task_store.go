package scheduledtasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
)

type Service struct {
	db            *sql.DB
	leaseDuration time.Duration
	maxSteers     int
	maxAttempts   int
	now           func() time.Time
}

func NewService(db *sql.DB, leaseDuration time.Duration, maxSteers, maxAttempts int) *Service {
	return &Service{db: db, leaseDuration: leaseDuration, maxSteers: maxSteers,
		maxAttempts: maxAttempts, now: time.Now}
}

type sessionContext struct {
	WorkspaceID     uuid.UUID
	ProjectID       uuid.UUID
	AdministratorID *uuid.UUID
	AgentProfileID  uuid.UUID
	Model           *string
	ReasoningEffort *string
	ServiceTier     string
	LifecycleState  string
}

func (s *Service) loadSessionContext(ctx context.Context, tool ToolContext) (sessionContext, error) {
	var result sessionContext
	var administrator, model, effort sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT session.workspace_id,session.workspace_project_id,
		session.created_by_administrator_id::text,session.agent_profile_id,session.model,
		session.reasoning_effort,session.service_tier,session.lifecycle_state
		FROM workspace_sessions session
		WHERE session.id=$1 AND session.workspace_project_id=$2`, tool.SessionID,
		tool.ProjectID).Scan(&result.WorkspaceID, &result.ProjectID, &administrator,
		&result.AgentProfileID, &model, &effort, &result.ServiceTier, &result.LifecycleState)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionContext{}, errors.New("当前工具调用不属于有效 Workspace Session")
	}
	if err != nil {
		return sessionContext{}, err
	}
	result.AdministratorID, err = nullableUUID(administrator)
	if err != nil {
		return sessionContext{}, err
	}
	result.Model = nullableString(model)
	result.ReasoningEffort = nullableString(effort)
	return result, nil
}

func validateTaskText(name, prompt string) error {
	name = strings.TrimSpace(name)
	prompt = strings.TrimSpace(prompt)
	if name == "" || len([]rune(name)) > 120 {
		return errors.New("任务名称必须为 1 到 120 个字符")
	}
	if prompt == "" || len([]rune(prompt)) > 100000 {
		return errors.New("任务 prompt 必须为 1 到 100000 个字符")
	}
	return nil
}

func validateSettings(settings SettingsInput) error {
	if settings.Model != nil && len(strings.TrimSpace(*settings.Model)) > 128 {
		return errors.New("模型名称过长")
	}
	if settings.ReasoningEffort != nil &&
		!codexsettings.ValidReasoningEffort(*settings.ReasoningEffort) {
		return errors.New("思考等级无效")
	}
	if settings.ServiceTier != nil {
		canonical, ok := codexsettings.CanonicalServiceTier(*settings.ServiceTier)
		if !ok || canonical == "" {
			return errors.New("服务等级无效")
		}
		*settings.ServiceTier = canonical
	}
	return nil
}

func (s *Service) Create(ctx context.Context, tool ToolContext, args ToolArguments) (Task, error) {
	if args.Kind == "" {
		args.Kind = KindHeartbeat
	}
	if args.Kind != KindStandalone && args.Kind != KindHeartbeat {
		return Task{}, errors.New("kind 必须是 standalone 或 heartbeat")
	}
	if args.Name == nil || args.Prompt == nil || args.Schedule == nil {
		return Task{}, errors.New("create 缺少 name、prompt 或 schedule")
	}
	if err := validateTaskText(*args.Name, *args.Prompt); err != nil {
		return Task{}, err
	}
	if len(*args.Schedule) > 65536 {
		return Task{}, errors.New("schedule 过长")
	}
	timezone := ""
	if args.Timezone != nil {
		timezone = strings.TrimSpace(*args.Timezone)
	}
	schedule, err := ParseSchedule(*args.Schedule, timezone)
	if err != nil {
		return Task{}, err
	}
	current, err := s.loadSessionContext(ctx, tool)
	if err != nil {
		return Task{}, err
	}
	if current.LifecycleState != "active" {
		return Task{}, errors.New("当前 Session 不可创建定时任务")
	}
	now := s.now().UTC()
	next := schedule.FirstAfter(now)
	status := StatusActive
	if next.IsZero() {
		status = StatusCompleted
	}

	task := Task{WorkspaceID: current.WorkspaceID, WorkspaceProjectID: current.ProjectID,
		CreatedByAdministratorID: current.AdministratorID, Kind: args.Kind,
		Name: strings.TrimSpace(*args.Name), Prompt: strings.TrimSpace(*args.Prompt),
		Status: status, ScheduleText: schedule.Text, Timezone: schedule.Timezone,
		ScheduleKind: schedule.Kind, ScheduleRevision: 1}
	if !next.IsZero() {
		task.NextRunAt = &next
	}
	if schedule.Interval > 0 {
		seconds := int64(schedule.Interval / time.Second)
		task.IntervalSeconds = &seconds
	}
	if args.Kind == KindHeartbeat {
		task.TargetSessionID = &tool.SessionID
		if args.Settings != nil {
			return Task{}, errors.New("heartbeat 始终继承 Session 设置，不能指定 settings")
		}
	} else {
		profile := current.AgentProfileID
		model, effort, tier := current.Model, current.ReasoningEffort, current.ServiceTier
		if args.Settings != nil {
			if err := validateSettings(*args.Settings); err != nil {
				return Task{}, err
			}
			if args.Settings.AgentProfileID != nil {
				profile = *args.Settings.AgentProfileID
			}
			model = normalizedOptional(args.Settings.Model, model)
			effort = normalizedOptional(args.Settings.ReasoningEffort, effort)
			if args.Settings.ServiceTier != nil {
				tier, _ = codexsettings.CanonicalServiceTier(*args.Settings.ServiceTier)
			}
		}
		if err := s.requireAgentProfile(ctx, profile); err != nil {
			return Task{}, err
		}
		task.AgentProfileID, task.Model, task.ReasoningEffort = &profile, model, effort
		task.ServiceTier = &tier
	}
	return s.insertTask(ctx, task)
}

func normalizedOptional(value, fallback *string) *string {
	if value == nil {
		return fallback
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func (s *Service) requireAgentProfile(ctx context.Context, id uuid.UUID) error {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_profiles WHERE id=$1)`,
		id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("agent profile 不存在")
	}
	return nil
}

func (s *Service) insertTask(ctx context.Context, task Task) (Task, error) {
	interval := any(nil)
	if task.IntervalSeconds != nil {
		interval = *task.IntervalSeconds
	}
	row := s.db.QueryRowContext(ctx, `INSERT INTO scheduled_tasks(
		workspace_id,workspace_project_id,target_session_id,created_by_administrator_id,
		kind,name,prompt,status,schedule_text,timezone,schedule_kind,interval_seconds,
		next_run_at,agent_profile_id,model,reasoning_effort,service_tier)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		RETURNING `+taskColumns, task.WorkspaceID, task.WorkspaceProjectID,
		nullableUUIDValue(task.TargetSessionID), nullableUUIDValue(task.CreatedByAdministratorID),
		task.Kind, task.Name, task.Prompt, task.Status, task.ScheduleText, task.Timezone,
		task.ScheduleKind, interval, task.NextRunAt, nullableUUIDValue(task.AgentProfileID),
		nullableText(task.Model), nullableText(task.ReasoningEffort), nullableText(task.ServiceTier))
	created, err := scanTask(row)
	if err != nil {
		return Task{}, fmt.Errorf("创建定时任务: %w", err)
	}
	return created, nil
}
