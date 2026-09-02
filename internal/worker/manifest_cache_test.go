package worker

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceManifestCacheRoundTripAndPermissions(t *testing.T) {
	root := t.TempDir()
	wanted := &workerprotocol.WorkspaceManifest{WorkspaceID: uuid.New(),
		Forums: []workerprotocol.WorkspaceForum{{ForumID: uuid.New(), GuildID: "guild"}}}
	require.NoError(t, SaveWorkspaceManifest(root, wanted))

	actual, err := LoadCachedWorkspaceManifest(root)
	require.NoError(t, err)
	require.Equal(t, wanted, actual)
	info, err := os.Stat(workspaceManifestPath(root))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestWorkspaceManifestCacheRejectsMissingOrInvalidSnapshot(t *testing.T) {
	root := t.TempDir()
	_, err := LoadCachedWorkspaceManifest(root)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Error(t, SaveWorkspaceManifest(root, &workerprotocol.WorkspaceManifest{}))
}
