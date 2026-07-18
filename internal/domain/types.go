package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type WorkItemKind string

const (
	WorkItemIssue       WorkItemKind = "issue"
	WorkItemPullRequest WorkItemKind = "pull_request"
)

type JobStatus string

const (
	JobSourceGitHubWorkItem      = "github_work_item"
	JobSourceDiscordConversation = "discord_conversation"
)

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobBlocked   JobStatus = "blocked"
	JobCanceled  JobStatus = "canceled"
)

type NormalizedEvent struct {
	Provider       string
	DeliveryID     string
	EventName      string
	Action         string
	InstallationID int64
	RepositoryID   int64
	Owner          string
	Repository     string
	Number         int
	Kind           WorkItemKind
	Title          string
	Actor          string
	ActorID        int64
	Body           string
	HeadSHA        string
	Raw            json.RawMessage
	ReceivedAt     time.Time
	Installation   SCMInstallationEvent
}

type SCMInstallationEvent struct {
	AccountLogin         string
	AccountType          string
	Repositories         []SCMRepository
	RemovedRepositoryIDs []int64
}

type SCMRepository struct {
	ExternalID    int64
	Owner         string
	Name          string
	DefaultBranch string
	CloneURL      string
}

type Job struct {
	ID                    uuid.UUID
	SourceType            string
	WorkItemID            uuid.UUID
	DiscordConversationID uuid.UUID
	DiscordMessageID      string
	RepositoryID          uuid.UUID
	AgentProfileID        uuid.UUID
	Status                JobStatus
	Instruction           string
	Skills                []string
	AllowedTools          []string
	DangerousActions      []string
	ActorLogin            string
	ActorPermission       string
	Attempt               int
	LeaseToken            string
	LeaseEpoch            int64
	LeaseExpiresAt        time.Time
	CreatedAt             time.Time
}

type ToolCall struct {
	ThreadID  string
	TurnID    string
	CallID    string
	Namespace string
	Tool      string
	Arguments json.RawMessage
}
