package orchestrator

import (
	"testing"

	"github.com/slovx2/tyrs-hand/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRuleHelpers(t *testing.T) {
	event := domain.NormalizedEvent{
		Owner: "owner", Repository: "repo", Number: 42, Actor: "alice",
		Body: "please fix", EventName: "issue_comment", Action: "created",
	}
	rendered := renderInstruction("{{owner}}/{{repository}}#{{number}} {{actor}} {{event}} {{action}} {{body}}", event)
	require.Equal(t, "owner/repo#42 alice issue_comment created please fix", rendered)
	require.True(t, mentions("Could @TYRS-HAND inspect?", "tyrs-hand"))
	require.True(t, mentions("(@tyrs-hand), inspect", "tyrs-hand"))
	require.False(t, mentions("no mention", "tyrs-hand"))
	require.False(t, mentions("@tyrs-hand-suffix inspect", "tyrs-hand"))
	require.False(t, mentions("prefix@tyrs-hand.example", "tyrs-hand"))
	require.False(t, mentions("@tyrs-hand", ""))
	require.True(t, isBot("tyrs-hand[bot]", "tyrs-hand"))
	require.True(t, isBot("TYRS-HAND", "tyrs-hand"))
	require.False(t, isBot("alice", "tyrs-hand"))
	require.False(t, isBot("", ""))
	require.Greater(t, permissionRank("admin"), permissionRank("write"))
	require.Greater(t, permissionRank("maintain"), permissionRank("push"))
	require.Greater(t, permissionRank("write"), permissionRank("triage"))
	require.Greater(t, permissionRank("triage"), permissionRank("read"))
	require.Equal(t, permissionRank("pull"), permissionRank("read"))
	require.Equal(t, 0, permissionRank("none"))
	require.JSONEq(t, `["a","b"]`, string(encode([]string{"a", "b"})))
}

func TestDiscordPermissionSyncForEvent(t *testing.T) {
	event := domain.NormalizedEvent{
		EventName: "installation_repositories", InstallationID: 42, RepositoryID: 100,
		Installation: domain.SCMInstallationEvent{
			Repositories:         []domain.SCMRepository{{ExternalID: 101}, {ExternalID: 100}},
			RemovedRepositoryIDs: []int64{102, 101},
		},
	}
	request := discordPermissionSyncForEvent(event)
	require.NotNil(t, request)
	require.Equal(t, int64(42), request.InstallationID)
	require.Equal(t, []int64{100, 101, 102}, request.RepositoryIDs)

	event.EventName = "membership"
	event.RepositoryID = 0
	event.Installation.Repositories = nil
	event.Installation.RemovedRepositoryIDs = nil
	request = discordPermissionSyncForEvent(event)
	require.NotNil(t, request)
	require.Empty(t, request.RepositoryIDs)

	event.EventName = "issue_comment"
	require.Nil(t, discordPermissionSyncForEvent(event))
}
