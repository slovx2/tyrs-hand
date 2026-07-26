package worker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeriveBrowserTokenScopes(t *testing.T) {
	worker, err := deriveBrowserToken("secret", "worker")
	require.NoError(t, err)
	require.Equal(t, "v1.worker.w3lxRQZQWESSFoA1cGQcumHLOF6yHToqOgUeybUSSiw", worker)
	environment, err := deriveBrowserToken("secret", "11111111-1111-4111-8111-111111111111")
	require.NoError(t, err)
	require.NotEqual(t, worker, environment)
	require.Error(t, func() error {
		_, cause := deriveBrowserToken("secret", "not-an-environment")
		return cause
	}())
}
