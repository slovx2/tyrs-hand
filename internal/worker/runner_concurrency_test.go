package worker

import (
	"testing"

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRunnerAllRoleClaimsOnlyGitHubJobs(t *testing.T) {
	runner := &Runner{cfg: config.Config{WorkerRole: "all"}}
	require.Equal(t, "github", runner.claimRole())
	require.Equal(t, []string{"github", "discord"}, runner.roles())
	require.True(t, runner.roleAllowed("github_work_item"))
	require.False(t, runner.roleAllowed("workspace_session"))
}

func TestRunnerDiscordOnlyRoleDoesNotClaimJobs(t *testing.T) {
	runner := &Runner{cfg: config.Config{WorkerRole: "discord"}}
	require.Empty(t, runner.claimRole())
	require.False(t, runner.roleAllowed("workspace_session"))
}
