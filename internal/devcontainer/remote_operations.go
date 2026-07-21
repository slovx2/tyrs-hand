package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

func (m *Manager) RunRemoteOperation(ctx context.Context, operation RemoteOperation) error {
	if !m.Enabled() {
		return errors.New("discord 开发容器未启用")
	}
	switch operation.Operation {
	case "rebuild":
		if err := m.removeDockerResource(ctx, "container", operation.ContainerName); err != nil {
			return err
		}
		return m.removeDockerResource(ctx, "image", operation.ImageRef)
	case "start":
		_, err := m.docker(ctx, "start", operation.ContainerName)
		return err
	case "stop":
		if !m.dockerResourceExists(ctx, "container", operation.ContainerName) {
			return nil
		}
		_, err := m.docker(ctx, "stop", "--time", "10", operation.ContainerName)
		return err
	case "delete_forum":
		return m.deleteRemoteForum(ctx, operation)
	case "delete_environment":
		if err := m.removeDockerResource(ctx, "container", operation.ContainerName); err != nil {
			return err
		}
		for _, volume := range []string{operation.DataVolume, operation.HomeVolume} {
			if err := m.removeDockerResource(ctx, "volume", volume); err != nil {
				return err
			}
		}
		if err := m.removeDockerResource(ctx, "network", operation.Network); err != nil {
			return err
		}
		return m.removeDockerResource(ctx, "image", operation.ImageRef)
	default:
		return fmt.Errorf("不支持的远程开发环境 Operation %q", operation.Operation)
	}
}

func (m *Manager) deleteRemoteForum(ctx context.Context, operation RemoteOperation) error {
	if !m.dockerResourceExists(ctx, "container", operation.ContainerName) {
		return nil
	}
	if _, err := m.docker(ctx, "start", operation.ContainerName); err != nil {
		return err
	}
	paths := []string{filepath.ToSlash(filepath.Join(containerRoot, operation.Workspace))}
	for _, conversationID := range operation.ConversationIDs {
		paths = append(paths, filepath.ToSlash(filepath.Join(containerRoot, "codex",
			conversationID.String())))
	}
	arguments := []string{"exec", "--user", "0:0", operation.ContainerName, "rm", "-rf"}
	_, err := m.docker(ctx, append(arguments, paths...)...)
	return err
}

func (m *Manager) dockerResourceExists(ctx context.Context, kind, name string) bool {
	if name == "" {
		return false
	}
	_, err := m.docker(ctx, kind, "inspect", name)
	return err == nil
}

func (m *Manager) removeDockerResource(ctx context.Context, kind, name string) error {
	if name == "" {
		return nil
	}
	if !m.dockerResourceExists(ctx, kind, name) {
		if _, err := m.docker(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
			return err
		}
		return nil
	}
	arguments := []string{kind, "rm"}
	if kind == "container" {
		arguments = append(arguments, "--force")
	}
	arguments = append(arguments, name)
	_, err := m.docker(ctx, arguments...)
	return err
}
