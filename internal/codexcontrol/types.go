package codexcontrol

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const WakeupChannel = "tyrs-hand:codex-controls"

const (
	SourceGitHub    = "github_work_item"
	SourceWorkspace = "workspace_session"
)

type IntentStatus string

const (
	IntentPlacementPending     IntentStatus = "placement_pending"
	IntentQueued               IntentStatus = "queued"
	IntentDispatching          IntentStatus = "dispatching"
	IntentAwaitingConfirmation IntentStatus = "awaiting_confirmation"
	IntentRunning              IntentStatus = "running"
	IntentReconciling          IntentStatus = "reconciling"
	IntentRetryWait            IntentStatus = "retry_wait"
	IntentCompleted            IntentStatus = "completed"
	IntentFailed               IntentStatus = "failed"
	IntentCanceled             IntentStatus = "canceled"
)

type Intent struct {
	ID                    uuid.UUID
	ControlID             uuid.UUID
	Sequence              int64
	Operation             string
	Behavior              string
	SourceType            string
	InputSurface          string
	WorkItemID            uuid.UUID
	DiscordConversationID uuid.UUID
	SessionID             uuid.UUID
	DiscordMessageID      string
	TargetIntentID        uuid.UUID
	ProjectionAnchor      string
	MessageEditRevision   int64
	ReplacementPhase      string
	RepositoryID          uuid.UUID
	ProjectID             uuid.UUID
	AgentProfileID        uuid.UUID
	Status                IntentStatus
	Instruction           string
	Skills                []string
	AllowedTools          []string
	DangerousActions      []string
	ActorLogin            string
	ActorPermission       string
	ActorParticipantID    uuid.UUID
	ActorDisplayName      string
	ReplyPolicy           string
	ReplyStatus           string
	Attempt               int
	MaxAttempts           int
	SubmissionID          string
	ConfirmedTurnID       string
	CreatedAt             time.Time
}

type ClaimedControl struct {
	Intent
	RunID             uuid.UUID
	Capability        string
	LeaseToken        string
	LeaseEpoch        int64
	LeaseExpiresAt    time.Time
	ExternalThreadID  string
	Recovering        bool
	CollaborationMode string
}

type EnqueueRequest struct {
	SourceType            string
	InputSurface          string
	WorkItemID            uuid.UUID
	DiscordConversationID uuid.UUID
	SessionID             uuid.UUID
	DiscordMessageID      string
	RepositoryID          uuid.UUID
	ProjectID             uuid.UUID
	AgentProfileID        uuid.UUID
	WebhookDeliveryID     uuid.UUID
	TriggerRuleID         uuid.UUID
	TriggerEvidence       json.RawMessage
	IdempotencyKey        string
	MessageLocalID        string
	Instruction           string
	DisplayInstruction    string
	Skills                []string
	AllowedTools          []string
	DangerousActions      []string
	Priority              int
	ActorLogin            string
	ActorPermission       string
	ActorParticipantID    uuid.UUID
	ActorDisplayName      string
	ReplyPolicy           string
	Operation             string
	Behavior              string
	TargetIntentID        uuid.UUID
	ProjectionAnchor      string
	MessageEditRevision   int64
	ReplacementPhase      string
}

type TurnResult struct {
	FinalAnswer     string      `json:"finalAnswer"`
	FinalOutputType string      `json:"finalOutputType,omitempty"`
	TurnID          string      `json:"turnId"`
	DurationMillis  int64       `json:"durationMillis"`
	Evidence        string      `json:"terminalEvidence"`
	AttachmentIDs   []uuid.UUID `json:"attachmentIds,omitempty"`
}
