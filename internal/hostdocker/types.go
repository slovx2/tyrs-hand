package hostdocker

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	RealDockerBinary = "/usr/local/libexec/tyrs-hand/docker"
	DockerSocket     = "/var/run/docker.sock"

	managedLabel    = "com.tyrs-hand.managed"
	workspaceLabel  = "com.tyrs-hand.workspace-id"
	intentLabel     = "com.tyrs-hand.intent-id"
	createdRunLabel = "com.tyrs-hand.created-run-id"

	envLeaseRoot   = "TYRS_HAND_DOCKER_LEASE_ROOT"
	envWorkspaceID = "TYRS_HAND_DOCKER_WORKSPACE_ID"
	envIntentID    = "TYRS_HAND_DOCKER_INTENT_ID"
	envRunID       = "TYRS_HAND_DOCKER_RUN_ID"
)

type Scope struct {
	WorkspaceID string
	IntentID    string
	RunID       string
}

func (s Scope) validate() error {
	for name, value := range map[string]string{
		"workspace": s.WorkspaceID, "intent": s.IntentID, "run": s.RunID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("docker %s ID 无效: %w", name, err)
		}
	}
	return nil
}

func (s Scope) labels() []string {
	return []string{
		managedLabel + "=true",
		workspaceLabel + "=" + s.WorkspaceID,
		intentLabel + "=" + s.IntentID,
		createdRunLabel + "=" + s.RunID,
	}
}

func (s Scope) namePrefix() string {
	compact := strings.ReplaceAll(s.WorkspaceID, "-", "")
	if len(compact) > 8 {
		compact = compact[:8]
	}
	return "th-" + compact + "-"
}

type runLease struct {
	Scope      Scope     `json:"scope"`
	Containers []string  `json:"containers,omitempty"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Ended      bool      `json:"ended"`
}

func (l runLease) active(now time.Time) bool {
	return !l.Ended && l.ExpiresAt.After(now)
}

func leaseRoot(dataRoot string) string {
	return filepath.Join(dataRoot, "state", "docker", "runs")
}

func leasePath(root, runID string) string { return filepath.Join(root, runID+".json") }
func lockPath(root, runID string) string  { return filepath.Join(root, runID+".lock") }

func (l runLease) encode() ([]byte, error) {
	data, err := json.Marshal(l)
	if err != nil {
		return nil, fmt.Errorf("编码 Docker Run Lease: %w", err)
	}
	return data, nil
}
