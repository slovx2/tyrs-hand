package workerprotocol

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

const Version = 25

// CodexTurnError 保留 Codex error 通知的结构化字段，供 Control 决定是否重试
// 并在 Discord 失败过程卡中展示。
type CodexTurnError struct {
	Message           string          `json:"message"`
	CodexErrorInfo    json.RawMessage `json:"codexErrorInfo,omitempty"`
	AdditionalDetails string          `json:"additionalDetails,omitempty"`
	WillRetry         bool            `json:"willRetry"`
	ThreadID          string          `json:"threadId"`
	TurnID            string          `json:"turnId"`
}

func (e *CodexTurnError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type EnrollRequest struct {
	Token string `json:"token"`
}

type EnrollResponse struct {
	WorkerID        uuid.UUID `json:"workerId"`
	Credential      string    `json:"credential"`
	ProtocolVersion int       `json:"protocolVersion"`
}

type HeartbeatRequest struct {
	WorkerVersion   string          `json:"workerVersion"`
	ProtocolVersion int             `json:"protocolVersion"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type SSHCredential struct {
	ID          uuid.UUID `json:"id"`
	PrivateKey  string    `json:"privateKey"`
	Passphrase  string    `json:"passphrase,omitempty"`
	PublicKey   string    `json:"publicKey"`
	Fingerprint string    `json:"fingerprint"`
}

type SSHHost struct {
	Alias          string    `json:"alias"`
	Hostname       string    `json:"hostname"`
	Port           int       `json:"port"`
	Username       string    `json:"username"`
	CredentialID   uuid.UUID `json:"credentialId"`
	ProxyJumpAlias string    `json:"proxyJumpAlias,omitempty"`
}

type SSHConfiguration struct {
	Revision    string          `json:"revision"`
	Credentials []SSHCredential `json:"credentials"`
	Hosts       []SSHHost       `json:"hosts"`
}

type ClaimRequest struct {
	Role string `json:"role"`
	Wait bool   `json:"wait"`
}

type ClaimResponse struct {
	Task *Task `json:"task,omitempty"`
}

type AppServerTunnelClaim struct {
	ID        uuid.UUID `json:"id"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type AppServerTunnelClaimResponse struct {
	Tunnel *AppServerTunnelClaim `json:"tunnel,omitempty"`
}

type MaterializationClaim struct {
	ID         uuid.UUID `json:"id"`
	Filename   string    `json:"filename"`
	MediaType  string    `json:"mediaType"`
	SizeBytes  int64     `json:"sizeBytes"`
	SHA256     string    `json:"sha256"`
	LeaseToken string    `json:"leaseToken"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type MaterializationClaimResponse struct {
	Materialization *MaterializationClaim `json:"materialization,omitempty"`
}

type MaterializationLeaseRequest struct {
	LeaseToken string `json:"leaseToken"`
}

type MaterializationCompleteRequest struct {
	LeaseToken string `json:"leaseToken"`
	RemotePath string `json:"remotePath"`
}

type MaterializationFailRequest struct {
	LeaseToken string `json:"leaseToken"`
	Error      string `json:"error"`
}

type WorkspaceManifest struct {
	WorkspaceID      uuid.UUID            `json:"workspaceId"`
	OwnerParticipant *ParticipantIdentity `json:"ownerParticipant,omitempty"`
	Forums           []WorkspaceForum     `json:"forums"`
}

type WorkspaceProjectSnapshot struct {
	Name         string `json:"name"`
	RelativePath string `json:"relativePath"`
	ProjectKind  string `json:"projectKind"`
	Branch       string `json:"branch,omitempty"`
	HeadSHA      string `json:"headSha,omitempty"`
	Dirty        bool   `json:"dirty"`
	RemoteURL    string `json:"remoteUrl,omitempty"`
}

type WorkspaceProjectSnapshotRequest struct {
	WorkspaceID uuid.UUID                  `json:"workspaceId"`
	Projects    []WorkspaceProjectSnapshot `json:"projects"`
	Error       string                     `json:"error,omitempty"`
}

type ParticipantIdentity struct {
	ParticipantID uuid.UUID `json:"participantId"`
	DiscordUserID string    `json:"discordUserId"`
	DisplayName   string    `json:"displayName"`
}

type WorkspaceForum struct {
	ForumID           uuid.UUID  `json:"forumId"`
	GuildID           string     `json:"guildId"`
	DiscordForumID    string     `json:"discordForumId"`
	OwnerUserID       string     `json:"ownerUserId"`
	ProjectID         *uuid.UUID `json:"projectId,omitempty"`
	WorkspaceKind     string     `json:"workspaceKind"`
	WorkspaceRelative string     `json:"workspaceRelative"`
	WorkspaceStatus   string     `json:"workspaceStatus"`
}

type Task struct {
	Claimed  codexcontrol.ClaimedControl `json:"claimed"`
	Snapshot TaskSnapshot                `json:"snapshot"`
}

type TaskSnapshot struct {
	GitHub      *GitHubSnapshot      `json:"github,omitempty"`
	GitHubAgent *GitHubAgentSnapshot `json:"githubAgent,omitempty"`
	Runtime     RuntimeSnapshot      `json:"runtime"`
}

type RuntimeSnapshot struct {
	ProfileName       string `json:"profileName"`
	Model             string `json:"model,omitempty"`
	ReasoningEffort   string `json:"reasoningEffort,omitempty"`
	ServiceTier       string `json:"serviceTier,omitempty"`
	Sandbox           string `json:"sandbox"`
	ApprovalPolicy    string `json:"approvalPolicy"`
	NetworkEnabled    bool   `json:"networkEnabled"`
	CollaborationMode string `json:"collaborationMode,omitempty"`
	SettingsRevision  int64  `json:"settingsRevision"`
}

type GitHubAgentSnapshot struct {
	Instructions string `json:"instructions,omitempty"`
}

type RuntimeSettingsApplied struct {
	Phase             string `json:"phase"`
	Model             string `json:"model,omitempty"`
	ReasoningEffort   string `json:"reasoningEffort,omitempty"`
	ServiceTier       string `json:"serviceTier,omitempty"`
	CollaborationMode string `json:"collaborationMode,omitempty"`
	SettingsRevision  int64  `json:"settingsRevision"`
}

type GitHubSnapshot struct {
	Owner          string `json:"owner"`
	Repository     string `json:"repository"`
	CloneURL       string `json:"cloneUrl"`
	DefaultBranch  string `json:"defaultBranch"`
	Kind           string `json:"kind"`
	Number         int    `json:"number"`
	HeadSHA        string `json:"headSha,omitempty"`
	HeadRef        string `json:"headRef,omitempty"`
	HeadRepository string `json:"headRepository,omitempty"`
	BaseSHA        string `json:"baseSha,omitempty"`
	BaseRef        string `json:"baseRef,omitempty"`
	HTMLURL        string `json:"htmlUrl,omitempty"`
}

type WorkspaceState struct {
	RunLeaseRequest
	CachePath    string `json:"cachePath"`
	WorktreePath string `json:"worktreePath"`
	Branch       string `json:"branch"`
	BaseSHA      string `json:"baseSha"`
	HeadSHA      string `json:"headSha"`
	Dirty        bool   `json:"dirty"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}

type RunLeaseRequest struct {
	LeaseToken string `json:"leaseToken"`
	LeaseEpoch int64  `json:"leaseEpoch"`
}

type RunHeartbeatResponse struct {
	Recovery RunRecoveryState `json:"recovery"`
}

type RunRecoveryState struct {
	Recovering       bool   `json:"recovering"`
	SubmissionID     string `json:"submissionId,omitempty"`
	ConfirmedTurnID  string `json:"confirmedTurnId,omitempty"`
	ExternalThreadID string `json:"externalThreadId,omitempty"`
}

type CompleteRequest struct {
	RunLeaseRequest
	IdempotencyKey string                  `json:"idempotencyKey"`
	Result         codexcontrol.TurnResult `json:"result"`
}

type FailRequest struct {
	RunLeaseRequest
	IdempotencyKey string          `json:"idempotencyKey"`
	Code           string          `json:"code"`
	Message        string          `json:"message"`
	CodexError     *CodexTurnError `json:"codexError,omitempty"`
}

type EventInput struct {
	Sequence int64           `json:"sequence"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
}

type EventsRequest struct {
	RunLeaseRequest
	Events []EventInput `json:"events"`
}

type SetThreadRequest struct {
	RunLeaseRequest
	ThreadID string `json:"threadId"`
}

type SubmissionRequest struct {
	RunLeaseRequest
	SubmissionID string `json:"submissionId"`
}

type ConfirmTurnRequest struct {
	RunLeaseRequest
	TurnID string `json:"turnId"`
}

type ToolCallRequest struct {
	RunLeaseRequest
	Capability string                `json:"capability"`
	Request    codex.ToolCallRequest `json:"request"`
}

type GitCredentialRequest struct {
	RunLeaseRequest
	Capability string `json:"capability"`
	Purpose    string `json:"purpose"`
	ThreadID   string `json:"threadId,omitempty"`
	TurnID     string `json:"turnId,omitempty"`
}
