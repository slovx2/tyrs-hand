package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBrowserAppServerTokenUsesWorkerScope(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "browser-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("secret\n"), 0o600))
	token, err := BrowserAppServerToken(config.Config{
		BrowserMCPURL: "http://127.0.0.1:8931/mcp", BrowserMCPTokenFile: tokenFile,
	})
	require.NoError(t, err)
	require.Equal(t, "v1.worker.w3lxRQZQWESSFoA1cGQcumHLOF6yHToqOgUeybUSSiw", token)

	token, err = BrowserAppServerToken(config.Config{})
	require.NoError(t, err)
	require.Empty(t, token)
}

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

func TestApplyBrowserMCPConfigUsesHostBrowserServerName(t *testing.T) {
	runtimeConfig := map[string]any{}
	applyBrowserMCPConfig(runtimeConfig, config.Config{
		BrowserMCPURL: "http://127.0.0.1:8931/mcp",
	}, "task-id")

	servers := runtimeConfig["mcp_servers"].(map[string]any)
	require.NotContains(t, servers, "browser")
	chrome := servers["chrome"].(map[string]any)
	require.Equal(t, "http://127.0.0.1:8931/mcp", chrome["url"])
	require.Equal(t, "TYRS_BROWSER_MCP_TOKEN", chrome["bearer_token_env_var"])
	require.Equal(t, map[string]string{"X-Tyrs-Browser-Task-Id": "task-id"},
		chrome["http_headers"])
}
