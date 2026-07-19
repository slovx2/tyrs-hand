package domain

import (
	"encoding/json"
	"time"
)

type WorkItemKind string

const (
	WorkItemIssue       WorkItemKind = "issue"
	WorkItemPullRequest WorkItemKind = "pull_request"
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
	Label          string
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

type ToolCall struct {
	ThreadID  string
	TurnID    string
	CallID    string
	Namespace string
	Tool      string
	Arguments json.RawMessage
}
