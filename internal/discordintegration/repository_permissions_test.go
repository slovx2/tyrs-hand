package discordintegration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRepositoryPermissionSyncMatching(t *testing.T) {
	forum := repositoryForumPermission{InstallationID: 42, RepositoryExternalID: 43}
	require.True(t, matchesRepositoryPermissionSync(forum, repositoryPermissionSync{}))
	require.True(t, matchesRepositoryPermissionSync(forum, repositoryPermissionSync{InstallationID: 42}))
	require.True(t, matchesRepositoryPermissionSync(forum, repositoryPermissionSync{RepositoryIDs: []int64{43}}))
	require.False(t, matchesRepositoryPermissionSync(forum, repositoryPermissionSync{InstallationID: 41}))
	require.False(t, matchesRepositoryPermissionSync(forum, repositoryPermissionSync{RepositoryIDs: []int64{44}}))
	require.False(t, matchesRepositoryPermissionSync(forum,
		repositoryPermissionSync{InstallationID: 41, RepositoryIDs: []int64{43}}))
}

func TestRepositoryPermissionSyncRejectsInvalidPayload(t *testing.T) {
	daemon := &Daemon{}
	require.Error(t, daemon.handleRepositoryPermissionSync(context.Background(), "guild", `{`))
	require.Error(t, daemon.handleRepositoryPermissionSync(context.Background(), "guild", `{}`))
}
