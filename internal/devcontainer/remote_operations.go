package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

func (m *Manager) ContainerID(ctx context.Context, name string) (string, error) {
	value, err := m.docker(ctx, "inspect", "--format", "{{.Id}}", name)
	return strings.TrimSpace(value), err
}

func (m *Manager) ImageID(ctx context.Context, reference string) (string, error) {
	value, err := m.docker(ctx, "image", "inspect", "--format", "{{.Id}}", reference)
	return strings.TrimSpace(value), err
}

func (m *Manager) RunRemoteOperation(ctx context.Context, operation RemoteOperation) error {
	if !m.Enabled() {
		return errors.New("discord 开发容器未启用")
	}
	switch operation.Operation {
	case "reconfigure":
		return m.reconfigureRemote(ctx, operation)
	case "rebase":
		identity, err := m.inspectDevelopmentImage(ctx, operation.ImageRef)
		if err != nil {
			return err
		}
		if operation.RuntimeUID > 0 && (operation.RuntimeUID != identity.UID ||
			operation.RuntimeGID != identity.GID || operation.RuntimeHome != identity.Home) {
			return errors.New("新开发镜像的 UID/GID 或 Home 与现有持久卷不兼容")
		}
		operation.RuntimeUser, operation.RuntimeUID = identity.User, identity.UID
		operation.RuntimeGID, operation.RuntimeHome = identity.GID, identity.Home
		return m.reconfigureRemote(ctx, operation)
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
		return nil
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
