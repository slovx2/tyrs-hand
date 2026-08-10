package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedBrowserWorkspaceRequiresHostWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "demo")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	resolved, err := managedBrowserWorkspace(root, workspace)
	require.NoError(t, err)
	expected, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	require.Equal(t, expected, resolved)

	outside := t.TempDir()
	_, err = managedBrowserWorkspace(root, outside)
	require.ErrorContains(t, err, "不属于宿主 Workspace")
}
