package devcontainer

import "github.com/google/uuid"

const (
	containerRoot = "/var/lib/tyrs-hand"
	runtimeRoot   = "/opt/tyrs-hand"
)

type Runtime struct {
	EnvironmentID uuid.UUID
	ForumID       uuid.UUID
	Container     string
	Workspace     string
	CodexHome     string
	User          string
	UID           int64
	GID           int64
	Home          string
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
