package workerconfig

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceProjectScanUsesOnlyConfiguredRoots(t *testing.T) {
	root := t.TempDir()
	codexHome := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "WakeQora"), 0o700))
	outside := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(outside, "must-not-scan"), 0o700))

	service := NewService(codexHome, "codex")
	service.SetWorkspaceRoot(root)
	value, err := handleRequest(context.Background(), service,
		"workspace.projects.scan", []byte(`{"path":"`+outside+`"}`))
	require.NoError(t, err)
	result, ok := value.(workerprotocol.WorkspaceProjectScanResult)
	require.True(t, ok)
	require.Empty(t, result.ScanError)
	require.Len(t, result.Projects, 2)
	require.Equal(t, "Workspace", result.Projects[0].Name)
	require.Equal(t, "workspace_root", result.Projects[0].ProjectSource)
	require.Equal(t, root, result.Projects[0].HostPath)
	require.Equal(t, "WakeQora", result.Projects[1].Name)
	require.Equal(t, "workspace_child", result.Projects[1].ProjectSource)
	require.NotContains(t, result.Projects, "must-not-scan")
}

func TestWorkspaceProjectScanRequiresConfiguredRoot(t *testing.T) {
	service := NewService(t.TempDir(), "codex")
	_, err := handleRequest(context.Background(), service,
		"workspace.projects.scan", nil)
	require.ErrorContains(t, err, "Workspace 根目录未配置")
}
