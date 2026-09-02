package worker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func workspaceManifestPath(root string) string {
	return filepath.Join(root, "control-state", "workspace.json")
}

// LoadCachedWorkspaceManifest 读取最近一次由 Control 确认的 Workspace 镜像。
func LoadCachedWorkspaceManifest(root string) (*workerprotocol.WorkspaceManifest, error) {
	data, err := os.ReadFile(workspaceManifestPath(root))
	if err != nil {
		return nil, err
	}
	var manifest workerprotocol.WorkspaceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if manifest.WorkspaceID == uuid.Nil {
		return nil, errors.New("本地 Workspace 快照无效")
	}
	return &manifest, nil
}

// SaveWorkspaceManifest 原子保存 Workspace 镜像，供 Control 离线时启动使用。
func SaveWorkspaceManifest(root string, manifest *workerprotocol.WorkspaceManifest) error {
	if manifest == nil || manifest.WorkspaceID == uuid.Nil {
		return errors.New("Workspace 快照无效")
	}
	path := workspaceManifestPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".workspace-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
