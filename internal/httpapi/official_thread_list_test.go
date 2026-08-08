package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOfficialThreadListIncludesEveryModelProvider(t *testing.T) {
	params := officialThreadListParams(true, "cursor-1")
	require.Equal(t, []string{}, params["modelProviders"])
	require.Equal(t, true, params["archived"])
	require.Equal(t, "cursor-1", params["cursor"])
}
