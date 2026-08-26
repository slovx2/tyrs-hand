package worker

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostWorkspacePathUsesConfiguredWorkspaceRoot(t *testing.T) {
	workspace, err := hostWorkspacePath("/home/worker/tyrs-hand/workspaces",
		"workspaces/tyrs-hand")
	require.NoError(t, err)
	require.Equal(t, "/home/worker/tyrs-hand/workspaces/tyrs-hand", workspace)

	for _, relative := range []string{"tyrs-hand", "workspaces/../secret", "workspaces/nested/project"} {
		_, err := hostWorkspacePath("/home/worker/tyrs-hand/workspaces", relative)
		require.Error(t, err, relative)
	}
}

func unwrapWorkerTestRequest(t *testing.T, request *http.Request) {
	t.Helper()
}
