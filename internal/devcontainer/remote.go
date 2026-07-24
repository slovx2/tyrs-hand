package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

var remoteEnvironmentLocks sync.Map

func (m *Manager) EnsureRemote(ctx context.Context, spec RemoteSpec,
	credential string, processEnvironment []string,
) (Runtime, RemoteState, error) {
	if !m.Enabled() {
		return Runtime{}, RemoteState{}, errors.New("discord 开发容器未启用")
	}
	unlock := LockRemoteEnvironment(spec.EnvironmentID)
	defer unlock()

	item := workspace{
		ForumID: spec.ForumID, Relative: spec.WorkspaceRelative,
		Status: spec.WorkspaceStatus, Branch: spec.WorkspaceBranch,
		Repository: spec.Repository, CloneURL: spec.CloneURL, DefaultRef: spec.DefaultRef,
		Environment: environment{
			ID: spec.EnvironmentID, Status: spec.EnvironmentStatus,
			ImageRef: spec.ImageRef, ImageID: spec.ImageID, ContainerName: spec.ContainerName,
			ContainerID: spec.ContainerID, DataVolume: spec.DataVolume,
			HomeVolume: spec.HomeVolume, Network: spec.Network, RuntimeUser: spec.RuntimeUser,
			RuntimeUID: spec.RuntimeUID, RuntimeGID: spec.RuntimeGID,
			RuntimeHome: spec.RuntimeHome,
		},
	}
	state := func(cause error) RemoteState {
		result := remoteState(item, spec.ConversationID)
		if cause != nil {
			result.Error = cause.Error()
		}
		return result
	}
	if item.Environment.Status == "pending" || item.Environment.Status == "error" ||
		item.Environment.ContainerID == "" {
		provisionErr := m.provision(ctx, &item, credential, processEnvironment)
		if provisionErr != nil {
			return Runtime{}, state(provisionErr), provisionErr
		}
	}
	if item.Status != "ready" {
		if err := m.cloneWorkspace(ctx, &item, credential); err != nil {
			return Runtime{}, state(err), err
		}
	}
	if _, err := m.docker(ctx, "start", item.Environment.ContainerName); err != nil {
		return Runtime{}, state(err), err
	}
	codexHome := filepath.ToSlash(filepath.Join(containerRoot, "codex"))
	if _, err := m.docker(ctx, "exec", "--user", "0:0", item.Environment.ContainerName,
		"mkdir", "-p", codexHome); err != nil {
		return Runtime{}, state(err), err
	}
	owner := fmt.Sprintf("%d:%d", item.Environment.RuntimeUID, item.Environment.RuntimeGID)
	if _, err := m.docker(ctx, "exec", "--user", "0:0", item.Environment.ContainerName,
		"chown", owner, codexHome); err != nil {
		return Runtime{}, state(err), err
	}
	runtime := Runtime{EnvironmentID: spec.EnvironmentID, ForumID: spec.ForumID,
		Container: item.Environment.ContainerName,
		Workspace: filepath.ToSlash(filepath.Join(containerRoot, item.Relative)), CodexHome: codexHome,
		User: item.Environment.RuntimeUser, UID: item.Environment.RuntimeUID,
		GID: item.Environment.RuntimeGID, Home: item.Environment.RuntimeHome,
		AppServerSocket: filepath.Join(m.developmentRuntimeDir, spec.EnvironmentID.String(), "app-server.sock"),
		RelaySocket:     filepath.Join(m.developmentRuntimeDir, spec.EnvironmentID.String(), "relay.sock")}
	result := state(nil)
	status, statusErr := m.Git(ctx, runtime, "status", "--porcelain=v1")
	head, headErr := m.Git(ctx, runtime, "rev-parse", "HEAD")
	if statusErr != nil {
		return runtime, result, statusErr
	}
	if headErr != nil {
		return runtime, result, headErr
	}
	result.WorkspaceDirty = strings.TrimSpace(status) != ""
	result.WorkspaceHeadSHA = strings.TrimSpace(head)
	return runtime, result, nil
}

func LockRemoteEnvironment(environmentID uuid.UUID) func() {
	lockValue, _ := remoteEnvironmentLocks.LoadOrStore(environmentID.String(), &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func remoteState(item workspace, conversationID uuid.UUID) RemoteState {
	return RemoteState{RemoteSpec: RemoteSpec{
		EnvironmentID: item.Environment.ID, ForumID: item.ForumID,
		ConversationID: conversationID, WorkspaceStatus: item.Status,
		WorkspaceRelative: item.Relative, WorkspaceBranch: item.Branch,
		Repository: item.Repository, CloneURL: item.CloneURL, DefaultRef: item.DefaultRef,
		EnvironmentStatus: item.Environment.Status, ImageRef: item.Environment.ImageRef,
		ImageID: item.Environment.ImageID, ContainerName: item.Environment.ContainerName,
		ContainerID: item.Environment.ContainerID, DataVolume: item.Environment.DataVolume,
		HomeVolume: item.Environment.HomeVolume, Network: item.Environment.Network,
		RuntimeUser: item.Environment.RuntimeUser, RuntimeUID: item.Environment.RuntimeUID,
		RuntimeGID: item.Environment.RuntimeGID, RuntimeHome: item.Environment.RuntimeHome,
	}}
}
