package devcontainer

import "github.com/google/uuid"

const (
	containerRoot = "/var/lib/tyrs-hand"
	runtimeRoot   = "/opt/tyrs-hand"
)

type Runtime struct {
	EnvironmentID   uuid.UUID
	ForumID         uuid.UUID
	Container       string
	Workspace       string
	CodexHome       string
	User            string
	UID             int64
	GID             int64
	Home            string
	AppServerSocket string
	RelaySocket     string
}

type RemoteSpec struct {
	EnvironmentID     uuid.UUID
	ForumID           uuid.UUID
	ConversationID    uuid.UUID
	WorkspaceStatus   string
	WorkspaceRelative string
	WorkspaceBranch   string
	Repository        string
	CloneURL          string
	DefaultRef        string
	BuildRepositoryID uuid.UUID
	BuildRepository   string
	BuildCloneURL     string
	BuildDefaultRef   string
	EnvironmentStatus string
	ImageRef          string
	ImageID           string
	ContainerName     string
	ContainerID       string
	DataVolume        string
	HomeVolume        string
	Network           string
	RuntimeUser       string
	RuntimeUID        int64
	RuntimeGID        int64
	RuntimeHome       string
	BuildSourceSHA    string
}

type RemoteState struct {
	RemoteSpec
	WorkspaceHeadSHA string
	WorkspaceDirty   bool
	Error            string
}

type RemoteOperation struct {
	EnvironmentID     uuid.UUID
	Operation         string
	ContainerName     string
	ImageRef          string
	DataVolume        string
	HomeVolume        string
	Network           string
	Workspace         string
	ConversationIDs   []uuid.UUID
	RuntimeUser       string
	RuntimeUID        int64
	RuntimeGID        int64
	RuntimeHome       string
	SSHPublicKey      string
	SSHPort           int
	SSHConfigRevision int64
}

type environment struct {
	ID                uuid.UUID
	BuildRepositoryID uuid.UUID
	BuildRepository   string
	BuildCloneURL     string
	BuildDefaultRef   string
	Status            string
	ImageRef          string
	ImageID           string
	ContainerName     string
	ContainerID       string
	DataVolume        string
	HomeVolume        string
	Network           string
	RuntimeUser       string
	RuntimeUID        int64
	RuntimeGID        int64
	RuntimeHome       string
	BuildSourceSHA    string
}

type workspace struct {
	ForumID     uuid.UUID
	Relative    string
	Status      string
	Branch      string
	Repository  string
	CloneURL    string
	DefaultRef  string
	Environment environment
}
