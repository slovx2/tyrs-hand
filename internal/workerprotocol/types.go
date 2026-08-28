package workerprotocol

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

const Version = 31

type WorkerRPCRequest struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type WorkerRPCResponse struct {
	ID     string `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type WorkerConfig struct {
	Revision         string         `json:"revision"`
	ModelProvider    string         `json:"-"`
	ModelProviders   map[string]any `json:"-"`
	BaseURL          string         `json:"baseUrl"`
	EnvKey           string         `json:"envKey"`
	APIKeyConfigured bool           `json:"apiKeyConfigured"`
	Agents           string         `json:"agents"`
}

type OAuthDevice struct {
	VerificationURL string    `json:"verificationUrl"`
	UserCode        string    `json:"userCode"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Status          string    `json:"status"`
	Account         string    `json:"account,omitempty"`
}

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
	WorkerVersion         string          `json:"workerVersion"`
	ProtocolVersion       int             `json:"protocolVersion"`
	SSHHostKeyFingerprint string          `json:"sshHostKeyFingerprint"`
	Metadata              json.RawMessage `json:"metadata,omitempty"`
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

type SessionTitleTask struct {
	ID             uuid.UUID `json:"id"`
	SessionID      uuid.UUID `json:"sessionId"`
	WorkspaceID    uuid.UUID `json:"workspaceId"`
	FirstMessage   string    `json:"firstMessage"`
	TitleRevision  int64     `json:"titleRevision"`
	Attempt        int       `json:"attempt"`
	LeaseToken     string    `json:"leaseToken"`
	LeaseExpiresAt time.Time `json:"leaseExpiresAt"`
}

type SessionTitleClaimResponse struct {
	Task *SessionTitleTask `json:"task,omitempty"`
}

type SessionTitleCompleteRequest struct {
	LeaseToken    string `json:"leaseToken"`
	TitleRevision int64  `json:"titleRevision"`
	Title         string `json:"title"`
}

type SessionTitleFailRequest struct {
	LeaseToken string `json:"leaseToken"`
	ErrorCode  string `json:"errorCode"`
}

type WorkspaceManifest struct {
	WorkspaceID      uuid.UUID            `json:"workspaceId"`
	OwnerParticipant *ParticipantIdentity `json:"ownerParticipant,omitempty"`
	Forums           []WorkspaceForum     `json:"forums"`
}

type WorkspaceProjectSnapshot struct {
	Name          string `json:"name"`
	RelativePath  string `json:"relativePath"`
	ProjectSource string `json:"projectSource"`
	HostPath      string `json:"hostPath,omitempty"`
	Available     bool   `json:"available"`
	ScanError     string `json:"scanError,omitempty"`
	ProjectKind   string `json:"projectKind"`
	Branch        string `json:"branch,omitempty"`
	HeadSHA       string `json:"headSha,omitempty"`
	Dirty         bool   `json:"dirty"`
	RemoteURL     string `json:"remoteUrl,omitempty"`
}

type WorkspaceProjectScanResult struct {
	Projects  []WorkspaceProjectSnapshot `json:"projects"`
	ScanError string                     `json:"scanError,omitempty"`
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

type DesktopThreadPrepareRequest struct {
	WorkspaceID   uuid.UUID       `json:"workspaceId"`
	WorkspaceRoot string          `json:"workspaceRoot,omitempty"`
	Operation     string          `json:"operation"`
	RequestKey    string          `json:"requestKey"`
	Params        json.RawMessage `json:"params"`
}

type DesktopThreadConfig struct {
	Model            string   `json:"model,omitempty"`
	ReasoningEffort  string   `json:"reasoningEffort,omitempty"`
	ServiceTier      string   `json:"serviceTier,omitempty"`
	AllowedTools     []string `json:"allowedTools"`
	DangerousActions []string `json:"dangerousActions"`
}

type DesktopThreadState struct {
	ID               uuid.UUID           `json:"id"`
	WorkspaceID      uuid.UUID           `json:"workspaceId"`
	Operation        string              `json:"operation"`
	Status           string              `json:"status"`
	ForumID          uuid.UUID           `json:"forumId,omitempty"`
	ConversationID   uuid.UUID           `json:"conversationId,omitempty"`
	ControlID        uuid.UUID           `json:"controlId,omitempty"`
	ExternalThreadID string              `json:"externalThreadId,omitempty"`
	Response         json.RawMessage     `json:"response,omitempty"`
	Error            string              `json:"error,omitempty"`
	Config           DesktopThreadConfig `json:"config"`
}

type DesktopThreadCompleteRequest struct {
	WorkspaceID uuid.UUID       `json:"workspaceId"`
	Response    json.RawMessage `json:"response"`
}

type DesktopThreadFailRequest struct {
	WorkspaceID uuid.UUID `json:"workspaceId"`
	Error       string    `json:"error"`
}

type ThreadMetadataEvent struct {
	ThreadID          string `json:"threadId"`
	Sequence          int64  `json:"sequence"`
	Kind              string `json:"kind"`
	Source            string `json:"source"`
	Name              string `json:"name,omitempty"`
	LifecycleState    string `json:"lifecycleState,omitempty"`
	Model             string `json:"model,omitempty"`
	ReasoningEffort   string `json:"reasoningEffort,omitempty"`
	ServiceTier       string `json:"serviceTier,omitempty"`
	CollaborationMode string `json:"collaborationMode,omitempty"`
	SettingsRevision  int64  `json:"settingsRevision,omitempty"`
}

type ThreadMetadataRequest struct {
	WorkspaceID uuid.UUID             `json:"workspaceId"`
	Generation  int64                 `json:"generation"`
	Events      []ThreadMetadataEvent `json:"events"`
}

type ThreadNameUpdate struct {
	ControlID   uuid.UUID `json:"controlId"`
	WorkspaceID uuid.UUID `json:"workspaceId"`
	ThreadID    string    `json:"threadId"`
	Name        string    `json:"name"`
	Revision    int64     `json:"revision"`
}

type ThreadNameAckRequest struct {
	WorkspaceID uuid.UUID `json:"workspaceId"`
	Revision    int64     `json:"revision"`
	Error       string    `json:"error,omitempty"`
}

type ThreadLifecyclePrepareRequest struct {
	WorkspaceID  uuid.UUID `json:"workspaceId"`
	ThreadID     string    `json:"threadId"`
	DesiredState string    `json:"desiredState"`
}

type ThreadLifecycleState struct {
	ID           uuid.UUID       `json:"id"`
	ControlID    uuid.UUID       `json:"controlId"`
	WorkspaceID  uuid.UUID       `json:"workspaceId"`
	ThreadID     string          `json:"threadId"`
	DesiredState string          `json:"desiredState"`
	Status       string          `json:"status"`
	Revision     int64           `json:"revision"`
	Response     json.RawMessage `json:"response,omitempty"`
	Error        string          `json:"error,omitempty"`
}

type ThreadLifecycleCompleteRequest struct {
	WorkspaceID uuid.UUID       `json:"workspaceId"`
	Response    json.RawMessage `json:"response,omitempty"`
	Error       string          `json:"error,omitempty"`
}

type DesktopTurnPrepareRequest struct {
	WorkspaceID uuid.UUID       `json:"workspaceId"`
	RequestKey  string          `json:"requestKey"`
	Params      json.RawMessage `json:"params"`
	Images      []DesktopImage  `json:"images,omitempty"`
	ImageError  string          `json:"imageError,omitempty"`
}

type DesktopImage struct {
	Filename   string `json:"filename"`
	MediaType  string `json:"mediaType,omitempty"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Error      string `json:"error,omitempty"`
	SourcePath string `json:"-"`
	Temporary  bool   `json:"-"`
}

type DesktopImageTarget struct {
	Status string `json:"status"`
}

type DesktopImageUploadMetadata struct {
	FinalAttempt bool `json:"finalAttempt"`
}

type DesktopImageUploadResult struct {
	Status       string `json:"status"`
	AttachmentID string `json:"attachmentId,omitempty"`
}

type AgentAttachmentUploadResult struct {
	AttachmentID uuid.UUID `json:"attachmentId"`
	Deduplicated bool      `json:"deduplicated"`
}

type DesktopImageFailureRequest struct {
	Error string `json:"error"`
}

const (
	DesktopImageCountLimit = 10
	DesktopImageFileLimit  = 10 << 20
	DesktopImageTotalLimit = 25 << 20
)

type DesktopRollbackPrepareRequest struct {
	WorkspaceID uuid.UUID       `json:"workspaceId"`
	RequestKey  string          `json:"requestKey"`
	Params      json.RawMessage `json:"params"`
}

type DesktopRollbackState struct {
	ID           uuid.UUID       `json:"id"`
	WorkspaceID  uuid.UUID       `json:"workspaceId"`
	ThreadID     string          `json:"threadId"`
	Status       string          `json:"status"`
	TargetTurnID string          `json:"targetTurnId"`
	Params       json.RawMessage `json:"params,omitempty"`
}

type DesktopRollbackCompleteRequest struct {
	WorkspaceID uuid.UUID       `json:"workspaceId"`
	Response    json.RawMessage `json:"response,omitempty"`
	Error       string          `json:"error,omitempty"`
}

type DesktopTurnPreflightRequest struct {
	WorkspaceID uuid.UUID       `json:"workspaceId"`
	Params      json.RawMessage `json:"params"`
}

type DesktopTurnPreflightResponse struct {
	Params json.RawMessage `json:"params"`
}

type DesktopSteerRecordRequest struct {
	WorkspaceID uuid.UUID       `json:"workspaceId"`
	RequestKey  string          `json:"requestKey"`
	Params      json.RawMessage `json:"params"`
}

type InteractiveRegisterRequest struct {
	RunLeaseRequest
	RequestID           json.RawMessage `json:"requestId"`
	Params              json.RawMessage `json:"params"`
	AppServerGeneration int64           `json:"appServerGeneration"`
}

type InteractiveAnswerRequest struct {
	WorkspaceID uuid.UUID       `json:"workspaceId"`
	ThreadID    string          `json:"threadId"`
	TurnID      string          `json:"turnId"`
	ItemID      string          `json:"itemId"`
	Surface     string          `json:"surface"`
	Answer      json.RawMessage `json:"answer"`
}

type InteractiveState struct {
	ID         uuid.UUID       `json:"id"`
	Status     string          `json:"status"`
	Questions  json.RawMessage `json:"questions,omitempty"`
	Answer     json.RawMessage `json:"answer,omitempty"`
	DeadlineAt *time.Time      `json:"deadlineAt,omitempty"`
	Secret     bool            `json:"secret"`
	Surface    string          `json:"surface,omitempty"`
	Accepted   bool            `json:"accepted,omitempty"`
	Ready      bool            `json:"ready"`
}

type Task struct {
	Claimed  codexcontrol.ClaimedControl `json:"claimed"`
	Snapshot TaskSnapshot                `json:"snapshot"`
}

type TaskSnapshot struct {
	GitHub      *GitHubSnapshot      `json:"github,omitempty"`
	GitHubAgent *GitHubAgentSnapshot `json:"githubAgent,omitempty"`
	Session     *SessionSnapshot     `json:"session,omitempty"`
	Discord     *DiscordSnapshot     `json:"discord,omitempty"`
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

type DiscordSnapshot struct {
	GuildID        string                   `json:"guildId"`
	ThreadID       string                   `json:"threadId"`
	MessageID      string                   `json:"messageId"`
	OwnerUserID    string                   `json:"ownerUserId"`
	ForumID        uuid.UUID                `json:"forumId"`
	WorkspaceID    uuid.UUID                `json:"workspaceId"`
	Body           string                   `json:"body"`
	UserID         string                   `json:"userId"`
	DisplayName    string                   `json:"displayName"`
	Username       string                   `json:"username"`
	GitHubUserID   int64                    `json:"githubUserId,omitempty"`
	GitHubLogin    string                   `json:"githubLogin,omitempty"`
	BindingID      string                   `json:"bindingId,omitempty"`
	BindingVersion int64                    `json:"bindingVersion,omitempty"`
	Access         string                   `json:"access"`
	Attachments    []Attachment             `json:"attachments,omitempty"`
	Project        *WorkspaceProjectContext `json:"project,omitempty"`
}

type SessionSnapshot struct {
	SessionID     uuid.UUID                `json:"sessionId"`
	MessageID     string                   `json:"messageId"`
	Body          string                   `json:"body"`
	ParticipantID uuid.UUID                `json:"participantId,omitempty"`
	DisplayName   string                   `json:"displayName,omitempty"`
	InputSurface  string                   `json:"inputSurface"`
	Attachments   []Attachment             `json:"attachments,omitempty"`
	Project       *WorkspaceProjectContext `json:"project"`
}

type WorkspaceProjectContext struct {
	WorkspaceID       uuid.UUID `json:"workspaceId"`
	ForumID           uuid.UUID `json:"forumId"`
	ConversationID    uuid.UUID `json:"conversationId"`
	WorkspaceRelative string    `json:"workspaceRelative"`
	WorkspaceBranch   string    `json:"workspaceBranch"`
	WorkspaceKind     string    `json:"workspaceKind"`
	ProjectSource     string    `json:"projectSource,omitempty"`
	HostPath          string    `json:"hostPath,omitempty"`
	ProjectID         uuid.UUID `json:"projectId,omitempty"`
	Repository        string    `json:"repository"`
	CloneURL          string    `json:"cloneUrl"`
	DefaultRef        string    `json:"defaultRef"`
}

type WorkspaceProjectState struct {
	RunLeaseRequest
	WorkspaceID      uuid.UUID `json:"workspaceId"`
	ProjectID        uuid.UUID `json:"projectId"`
	WorkspaceHeadSHA string    `json:"workspaceHeadSha,omitempty"`
	WorkspaceDirty   bool      `json:"workspaceDirty"`
	Error            string    `json:"error,omitempty"`
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

type Attachment struct {
	ID        uuid.UUID `json:"id"`
	Kind      string    `json:"kind"`
	Filename  string    `json:"filename"`
	MediaType string    `json:"mediaType"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
}

type RunLeaseRequest struct {
	LeaseToken string `json:"leaseToken"`
	LeaseEpoch int64  `json:"leaseEpoch"`
}

type RunCommand struct {
	ID          uuid.UUID        `json:"id"`
	Sequence    int64            `json:"sequence"`
	Operation   string           `json:"operation"`
	Instruction string           `json:"instruction,omitempty"`
	Session     *SessionSnapshot `json:"session,omitempty"`
	Discord     *DiscordSnapshot `json:"discord,omitempty"`
}

type RunHeartbeatResponse struct {
	Commands []RunCommand     `json:"commands"`
	Recovery RunRecoveryState `json:"recovery"`
}

type RunRecoveryState struct {
	Recovering       bool   `json:"recovering"`
	SubmissionID     string `json:"submissionId,omitempty"`
	ConfirmedTurnID  string `json:"confirmedTurnId,omitempty"`
	ExternalThreadID string `json:"externalThreadId,omitempty"`
}

type CommandAckRequest struct {
	RunLeaseRequest
	CommandID uuid.UUID `json:"commandId"`
	Action    string    `json:"action"`
	TurnID    string    `json:"turnId,omitempty"`
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
