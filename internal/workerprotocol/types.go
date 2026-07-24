package workerprotocol

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

const Version = 7

type EnrollRequest struct {
	Token string `json:"token"`
}

type EnrollResponse struct {
	NodeID          uuid.UUID `json:"nodeId"`
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
	WorkerID string `json:"workerId"`
	Role     string `json:"role"`
	Wait     bool   `json:"wait"`
}

type ClaimResponse struct {
	Task                 *Task                 `json:"task,omitempty"`
	DevelopmentOperation *DevelopmentOperation `json:"developmentOperation,omitempty"`
}

type DevelopmentOperation struct {
	ID                uuid.UUID   `json:"id"`
	Operation         string      `json:"operation"`
	LeaseToken        string      `json:"leaseToken"`
	LeaseEpoch        int64       `json:"leaseEpoch"`
	EnvironmentID     uuid.UUID   `json:"environmentId"`
	ForumID           *uuid.UUID  `json:"forumId,omitempty"`
	ContainerName     string      `json:"containerName"`
	ImageRef          string      `json:"imageRef,omitempty"`
	DataVolume        string      `json:"dataVolume"`
	HomeVolume        string      `json:"homeVolume"`
	Network           string      `json:"network"`
	Workspace         string      `json:"workspace,omitempty"`
	ConversationIDs   []uuid.UUID `json:"conversationIds,omitempty"`
	RuntimeUser       string      `json:"runtimeUser,omitempty"`
	RuntimeUID        int64       `json:"runtimeUid,omitempty"`
	RuntimeGID        int64       `json:"runtimeGid,omitempty"`
	RuntimeHome       string      `json:"runtimeHome,omitempty"`
	SSHPublicKey      string      `json:"sshPublicKey,omitempty"`
	SSHPort           int         `json:"sshPort,omitempty"`
	SSHConfigRevision int64       `json:"sshConfigRevision"`
	AppliedRevision   int64       `json:"appliedRevision,omitempty"`
	ContainerID       string      `json:"containerId,omitempty"`
	DaemonStatus      string      `json:"daemonStatus,omitempty"`
}

type DevelopmentOperationLease struct {
	LeaseToken string `json:"leaseToken"`
	LeaseEpoch int64  `json:"leaseEpoch"`
}

type DevelopmentOperationTerminal struct {
	DevelopmentOperationLease
	IdempotencyKey  string `json:"idempotencyKey"`
	Error           string `json:"error,omitempty"`
	AppliedRevision int64  `json:"appliedRevision,omitempty"`
	ContainerID     string `json:"containerId,omitempty"`
	DaemonStatus    string `json:"daemonStatus,omitempty"`
}

type EnvironmentManifest struct {
	EnvironmentID     uuid.UUID            `json:"environmentId"`
	ContainerName     string               `json:"containerName"`
	ContainerID       string               `json:"containerId,omitempty"`
	ImageRef          string               `json:"imageRef,omitempty"`
	DataVolume        string               `json:"dataVolume"`
	HomeVolume        string               `json:"homeVolume"`
	Network           string               `json:"network"`
	RuntimeUser       string               `json:"runtimeUser,omitempty"`
	RuntimeUID        int64                `json:"runtimeUid,omitempty"`
	RuntimeGID        int64                `json:"runtimeGid,omitempty"`
	RuntimeHome       string               `json:"runtimeHome,omitempty"`
	SSHPublicKey      string               `json:"sshPublicKey,omitempty"`
	SSHPort           int                  `json:"sshPort,omitempty"`
	SSHConfigRevision int64                `json:"sshConfigRevision"`
	AppliedRevision   int64                `json:"appliedRevision"`
	SSHParticipant    *ParticipantIdentity `json:"sshParticipant,omitempty"`
	Forums            []EnvironmentForum   `json:"forums"`
}

type ParticipantIdentity struct {
	ParticipantID uuid.UUID `json:"participantId"`
	DiscordUserID string    `json:"discordUserId"`
	DisplayName   string    `json:"displayName"`
}

type EnvironmentForum struct {
	ForumID           uuid.UUID `json:"forumId"`
	GuildID           string    `json:"guildId"`
	DiscordForumID    string    `json:"discordForumId"`
	OwnerUserID       string    `json:"ownerUserId"`
	RepositoryID      uuid.UUID `json:"repositoryId"`
	WorkspaceRelative string    `json:"workspaceRelative"`
	WorkspaceStatus   string    `json:"workspaceStatus"`
}

type EnvironmentDaemonState struct {
	EnvironmentID   uuid.UUID `json:"environmentId"`
	Status          string    `json:"status"`
	AppServerStatus string    `json:"appServerStatus"`
	SSHStatus       string    `json:"sshStatus"`
	RelayStatus     string    `json:"relayStatus"`
	Error           string    `json:"error,omitempty"`
}

type DesktopThreadPrepareRequest struct {
	EnvironmentID uuid.UUID       `json:"environmentId"`
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
	EnvironmentID    uuid.UUID           `json:"environmentId"`
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
	EnvironmentID uuid.UUID       `json:"environmentId"`
	Response      json.RawMessage `json:"response"`
}

type DesktopThreadFailRequest struct {
	EnvironmentID uuid.UUID `json:"environmentId"`
	Error         string    `json:"error"`
}

type ThreadMetadataEvent struct {
	ThreadID        string `json:"threadId"`
	Sequence        int64  `json:"sequence"`
	Kind            string `json:"kind"`
	Name            string `json:"name,omitempty"`
	LifecycleState  string `json:"lifecycleState,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	ServiceTier     string `json:"serviceTier,omitempty"`
}

type ThreadMetadataRequest struct {
	EnvironmentID uuid.UUID             `json:"environmentId"`
	Generation    int64                 `json:"generation"`
	Events        []ThreadMetadataEvent `json:"events"`
}

type ThreadNameUpdate struct {
	ControlID     uuid.UUID `json:"controlId"`
	EnvironmentID uuid.UUID `json:"environmentId"`
	ThreadID      string    `json:"threadId"`
	Name          string    `json:"name"`
	Revision      int64     `json:"revision"`
}

type ThreadNameAckRequest struct {
	EnvironmentID uuid.UUID `json:"environmentId"`
	Revision      int64     `json:"revision"`
	Error         string    `json:"error,omitempty"`
}

type ThreadLifecyclePrepareRequest struct {
	EnvironmentID uuid.UUID `json:"environmentId"`
	ThreadID      string    `json:"threadId"`
	DesiredState  string    `json:"desiredState"`
}

type ThreadLifecycleState struct {
	ID            uuid.UUID       `json:"id"`
	ControlID     uuid.UUID       `json:"controlId"`
	EnvironmentID uuid.UUID       `json:"environmentId"`
	ThreadID      string          `json:"threadId"`
	DesiredState  string          `json:"desiredState"`
	Status        string          `json:"status"`
	Revision      int64           `json:"revision"`
	Response      json.RawMessage `json:"response,omitempty"`
	Error         string          `json:"error,omitempty"`
}

type ThreadLifecycleCompleteRequest struct {
	EnvironmentID uuid.UUID       `json:"environmentId"`
	Response      json.RawMessage `json:"response,omitempty"`
	Error         string          `json:"error,omitempty"`
}

type DesktopTurnPrepareRequest struct {
	EnvironmentID uuid.UUID       `json:"environmentId"`
	WorkerID      string          `json:"workerId"`
	RequestKey    string          `json:"requestKey"`
	Params        json.RawMessage `json:"params"`
}

type DesktopSteerRecordRequest struct {
	EnvironmentID uuid.UUID       `json:"environmentId"`
	RequestKey    string          `json:"requestKey"`
	Params        json.RawMessage `json:"params"`
}

type InteractiveRegisterRequest struct {
	RunLeaseRequest
	RequestID           json.RawMessage `json:"requestId"`
	Params              json.RawMessage `json:"params"`
	AppServerGeneration int64           `json:"appServerGeneration"`
}

type InteractiveAnswerRequest struct {
	EnvironmentID uuid.UUID       `json:"environmentId"`
	ThreadID      string          `json:"threadId"`
	TurnID        string          `json:"turnId"`
	ItemID        string          `json:"itemId"`
	Surface       string          `json:"surface"`
	Answer        json.RawMessage `json:"answer"`
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
	GitHub  *GitHubSnapshot  `json:"github,omitempty"`
	Discord *DiscordSnapshot `json:"discord,omitempty"`
	Runtime RuntimeSnapshot  `json:"runtime"`
}

type RuntimeSnapshot struct {
	ProfileName     string `json:"profileName"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	ServiceTier     string `json:"serviceTier,omitempty"`
	Sandbox         string `json:"sandbox"`
	ApprovalPolicy  string `json:"approvalPolicy"`
	NetworkEnabled  bool   `json:"networkEnabled"`
	ModelSource     string `json:"modelSource"`
	BaseURL         string `json:"baseUrl,omitempty"`
	ProxyURL        string `json:"proxyUrl,omitempty"`
	ConfigSignature string `json:"configSignature"`
	GlobalAgents    string `json:"globalAgents"`
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
	GuildID        string           `json:"guildId"`
	ThreadID       string           `json:"threadId"`
	MessageID      string           `json:"messageId"`
	OwnerUserID    string           `json:"ownerUserId"`
	ForumID        uuid.UUID        `json:"forumId"`
	EnvironmentID  uuid.UUID        `json:"environmentId"`
	Body           string           `json:"body"`
	UserID         string           `json:"userId"`
	DisplayName    string           `json:"displayName"`
	Username       string           `json:"username"`
	GitHubUserID   int64            `json:"githubUserId,omitempty"`
	GitHubLogin    string           `json:"githubLogin,omitempty"`
	BindingID      string           `json:"bindingId,omitempty"`
	BindingVersion int64            `json:"bindingVersion,omitempty"`
	Access         string           `json:"access"`
	Attachments    []Attachment     `json:"attachments,omitempty"`
	Development    *DevelopmentSpec `json:"development,omitempty"`
}

type DevelopmentSpec struct {
	EnvironmentID     uuid.UUID `json:"environmentId"`
	ForumID           uuid.UUID `json:"forumId"`
	ConversationID    uuid.UUID `json:"conversationId"`
	WorkspaceStatus   string    `json:"workspaceStatus"`
	WorkspaceRelative string    `json:"workspaceRelative"`
	WorkspaceBranch   string    `json:"workspaceBranch"`
	Repository        string    `json:"repository"`
	CloneURL          string    `json:"cloneUrl"`
	DefaultRef        string    `json:"defaultRef"`
	BuildRepositoryID uuid.UUID `json:"buildRepositoryId"`
	BuildRepository   string    `json:"buildRepository"`
	BuildCloneURL     string    `json:"buildCloneUrl"`
	BuildDefaultRef   string    `json:"buildDefaultRef"`
	EnvironmentStatus string    `json:"environmentStatus"`
	ImageRef          string    `json:"imageRef,omitempty"`
	ImageID           string    `json:"imageId,omitempty"`
	ContainerName     string    `json:"containerName"`
	ContainerID       string    `json:"containerId,omitempty"`
	DataVolume        string    `json:"dataVolume"`
	HomeVolume        string    `json:"homeVolume"`
	Network           string    `json:"network"`
	RuntimeUser       string    `json:"runtimeUser,omitempty"`
	RuntimeUID        int64     `json:"runtimeUid,omitempty"`
	RuntimeGID        int64     `json:"runtimeGid,omitempty"`
	RuntimeHome       string    `json:"runtimeHome,omitempty"`
	BuildSourceSHA    string    `json:"buildSourceSha,omitempty"`
}

type DevelopmentState struct {
	RunLeaseRequest
	DevelopmentSpec
	WorkspaceHeadSHA string `json:"workspaceHeadSha,omitempty"`
	WorkspaceDirty   bool   `json:"workspaceDirty"`
	Error            string `json:"error,omitempty"`
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
	Discord     *DiscordSnapshot `json:"discord,omitempty"`
}

type RunHeartbeatResponse struct {
	Commands []RunCommand     `json:"commands"`
	Recovery RunRecoveryState `json:"recovery"`
}

type RunRecoveryState struct {
	Recovering        bool   `json:"recovering"`
	SubmissionID      string `json:"submissionId,omitempty"`
	ConfirmedTurnID   string `json:"confirmedTurnId,omitempty"`
	ExternalThreadID  string `json:"externalThreadId,omitempty"`
	CodexHomeKey      string `json:"codexHomeKey,omitempty"`
	ProviderSignature string `json:"providerSignature,omitempty"`
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
	IdempotencyKey string `json:"idempotencyKey"`
	Code           string `json:"code"`
	Message        string `json:"message"`
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

type RuntimeCredential struct {
	APIKey              string          `json:"apiKey,omitempty"`
	BaseURL             string          `json:"baseUrl,omitempty"`
	ProxyURL            string          `json:"proxyUrl,omitempty"`
	ModelSource         string          `json:"modelSource"`
	ChatGPTAuth         json.RawMessage `json:"chatgptAuth,omitempty"`
	ChatGPTAuthRevision int64           `json:"chatgptAuthRevision"`
	ConfigSignature     string          `json:"configSignature,omitempty"`
	GlobalAgents        string          `json:"globalAgents,omitempty"`
}

type SetThreadRequest struct {
	RunLeaseRequest
	ThreadID          string `json:"threadId"`
	CodexHome         string `json:"codexHome"`
	ProviderSignature string `json:"providerSignature"`
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
