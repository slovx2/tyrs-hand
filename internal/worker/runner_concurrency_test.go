package worker

import (
	"testing"

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRunnerUsesSingleAllClaim(t *testing.T) {
	runner := &Runner{cfg: config.Config{WorkerRole: "all"}}
	require.Equal(t, "all", runner.claimRole())
	require.Equal(t, []string{"github", "discord"}, runner.roles())
}
