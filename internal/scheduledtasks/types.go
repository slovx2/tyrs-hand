package scheduledtasks

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	KindStandalone = "standalone"
	KindHeartbeat  = "heartbeat"

	StatusActive    = "active"
	StatusPaused    = "paused"
	StatusCompleted = "completed"
	StatusDeleted   = "deleted"
)

type Task struct {
	ID                       uuid.UUID  `json:"id"`
	WorkspaceID              uuid.UUID  `json:"workspaceId"`
	WorkspaceProjectID       uuid.UUID  `json:"workspaceProjectId"`
	TargetSessionID          *uuid.UUID `json:"targetSessionId,omitempty"`
	CreatedByAdministratorID *uuid.UUID `json:"createdByAdministratorId,omitempty"`
	Kind                     string     `json:"kind"`
	Name                     string     `json:"name"`
	Prompt                   string     `json:"prompt"`
	Status                   string     `json:"status"`
	ScheduleText             string     `json:"schedule"`
	Timezone                 string     `json:"timezone"`
	ScheduleKind             string     `json:"scheduleKind"`
	IntervalSeconds          *int64     `json:"intervalSeconds,omitempty"`
	NextRunAt                *time.Time `json:"nextRunAt,omitempty"`
	BlockedUntil             *time.Time `json:"blockedUntil,omitempty"`
	LastRunAt                *time.Time `json:"lastRunAt,omitempty"`
	AgentProfileID           *uuid.UUID `json:"agentProfileId,omitempty"`
	Model                    *string    `json:"model,omitempty"`
	ReasoningEffort          *string    `json:"reasoningEffort,omitempty"`
	ServiceTier              *string    `json:"serviceTier,omitempty"`
	ScheduleRevision         int64      `json:"scheduleRevision"`
	LastErrorCode            *string    `json:"lastErrorCode,omitempty"`
	LastErrorMessage         *string    `json:"lastErrorMessage,omitempty"`
	CreatedAt                time.Time  `json:"createdAt"`
	UpdatedAt                time.Time  `json:"updatedAt"`
}

type Run struct {
	ID               uuid.UUID       `json:"id"`
	ScheduledTaskID  uuid.UUID       `json:"scheduledTaskId"`
	ScheduleRevision int64           `json:"scheduleRevision"`
	Trigger          string          `json:"trigger"`
	TriggerKey       string          `json:"triggerKey"`
	ScheduledFor     time.Time       `json:"scheduledFor"`
	CoalescedThrough *time.Time      `json:"coalescedThrough,omitempty"`
	Status           string          `json:"status"`
	IntentID         *uuid.UUID      `json:"intentId,omitempty"`
	SessionID        *uuid.UUID      `json:"sessionId,omitempty"`
	TaskSnapshot     json.RawMessage `json:"taskSnapshot"`
	ErrorCode        *string         `json:"errorCode,omitempty"`
	ErrorMessage     *string         `json:"errorMessage,omitempty"`
	StartedAt        *time.Time      `json:"startedAt,omitempty"`
	FinishedAt       *time.Time      `json:"finishedAt,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

type ToolContext struct {
	RunID          uuid.UUID
	IntentID       uuid.UUID
	SessionID      uuid.UUID
	ProjectID      uuid.UUID
	AgentProfileID uuid.UUID
	ExternalThread string
	ThreadID       string
	TurnID         string
	CallID         string
}

type SettingsInput struct {
	AgentProfileID  *uuid.UUID `json:"agent_profile_id,omitempty"`
	Model           *string    `json:"model,omitempty"`
	ReasoningEffort *string    `json:"reasoning_effort,omitempty"`
	ServiceTier     *string    `json:"service_tier,omitempty"`
}

type ToolArguments struct {
	Action         string         `json:"action"`
	TaskID         *uuid.UUID     `json:"task_id,omitempty"`
	Kind           string         `json:"kind,omitempty"`
	Name           *string        `json:"name,omitempty"`
	Prompt         *string        `json:"prompt,omitempty"`
	Schedule       *string        `json:"schedule,omitempty"`
	Timezone       *string        `json:"timezone,omitempty"`
	Status         *string        `json:"status,omitempty"`
	Settings       *SettingsInput `json:"settings,omitempty"`
	IncludeDeleted bool           `json:"include_deleted,omitempty"`
}
