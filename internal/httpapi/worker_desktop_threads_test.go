package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDesktopWorkspacePathUsesConfiguredWorkspaceRoot(t *testing.T) {
	workspace, err := desktopWorkspacePath("/home/songsiyu/tyrs-hand/workspaces",
		"workspaces/tyrs-hand")
	require.NoError(t, err)
	require.Equal(t, "/home/songsiyu/tyrs-hand/workspaces/tyrs-hand", workspace)

	for _, relative := range []string{"tyrs-hand", "workspaces/../secret", "workspaces/nested/project"} {
		_, err := desktopWorkspacePath("/home/songsiyu/tyrs-hand/workspaces", relative)
		require.Error(t, err, relative)
	}
}
