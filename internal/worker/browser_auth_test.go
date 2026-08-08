package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/stretchr/testify/require"
)

func TestDeriveBrowserAppServerTokensUsesWorkspaceScopeForEverySurface(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "browser-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("secret\n"), 0o600))
	workspaceID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	tokens, err := DeriveBrowserAppServerTokens(config.Config{
		BrowserMCPURL: "http://127.0.0.1:8931/mcp", BrowserMCPTokenFile: tokenFile,
	}, workspaceID)
	require.NoError(t, err)
	require.Contains(t, tokens.Worker, "v1."+workspaceID.String()+".")
	require.Equal(t, tokens.Worker, tokens.Desktop)

	tokens, err = DeriveBrowserAppServerTokens(config.Config{}, uuid.Nil)
	require.NoError(t, err)
	require.Empty(t, tokens)
}

func TestDeriveBrowserAppServerTokensFallsBackToWorkerWithoutWorkspace(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "browser-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("secret\n"), 0o600))
	tokens, err := DeriveBrowserAppServerTokens(config.Config{
		BrowserMCPURL: "http://127.0.0.1:8931/mcp", BrowserMCPTokenFile: tokenFile,
	}, uuid.Nil)
	require.NoError(t, err)
	require.Equal(t, "v1.worker.w3lxRQZQWESSFoA1cGQcumHLOF6yHToqOgUeybUSSiw", tokens.Worker)
	require.Equal(t, tokens.Worker, tokens.Desktop)
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

func TestProcessorBrowserScopeUsesWorkspaceIdentity(t *testing.T) {
	workspaceID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	require.Equal(t, workspaceID.String(), (&Processor{workspaceID: workspaceID}).browserScope())
	require.Equal(t, "worker", (&Processor{}).browserScope())
}

func TestApplyBrowserMCPConfigUsesHostBrowserServerName(t *testing.T) {
	runtimeConfig := map[string]any{}
	applyBrowserMCPConfig(runtimeConfig, config.Config{
		BrowserMCPURL: "http://127.0.0.1:8931/mcp",
	}, codex.BrowserMCPWorkerTokenEnvironment, "task-id")

	servers := runtimeConfig["mcp_servers"].(map[string]any)
	require.NotContains(t, servers, "browser")
	chrome := servers["chrome"].(map[string]any)
	require.Equal(t, "http://127.0.0.1:8931/mcp", chrome["url"])
	require.Equal(t, codex.BrowserMCPWorkerTokenEnvironment,
		chrome["bearer_token_env_var"])
	require.Equal(t, map[string]string{"X-Tyrs-Browser-Task-Id": "task-id"},
		chrome["http_headers"])
}
