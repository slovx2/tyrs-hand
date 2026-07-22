package codexcontrol

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const WakeupChannel = "tyrs-hand:codex-controls"

const (
	SourceGitHub  = "github_work_item"
	SourceDiscord = "discord_conversation"
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
	DiscordMessageID      string
	RepositoryID          uuid.UUID
	AgentProfileID        uuid.UUID
	Status                IntentStatus
	Instruction           string
	Skills                []string
	AllowedTools          []string
	DangerousActions      []string
	ActorLogin            string
	ActorPermission       string
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
	CodexHomeKey      string
	ProviderSignature string
	Recovering        bool
}

type EnqueueRequest struct {
	SourceType            string
	InputSurface          string
	WorkItemID            uuid.UUID
	DiscordConversationID uuid.UUID
	DiscordMessageID      string
	RepositoryID          uuid.UUID
	AgentProfileID        uuid.UUID
	ContextVersion        int64
	WebhookDeliveryID     uuid.UUID
	TriggerRuleID         uuid.UUID
	TriggerEvidence       json.RawMessage
	IdempotencyKey        string
	Instruction           string
	Skills                []string
	AllowedTools          []string
	DangerousActions      []string
	Priority              int
	ActorLogin            string
	ActorPermission       string
	ReplyPolicy           string
	Operation             string
	Behavior              string
}

type TurnResult struct {
	FinalAnswer    string `json:"finalAnswer"`
	TurnID         string `json:"turnId"`
	DurationMillis int64  `json:"durationMillis"`
	Evidence       string `json:"terminalEvidence"`
}
