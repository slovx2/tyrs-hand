package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitHubAppManifestUsesSupportedEvents(t *testing.T) {
	manifest := githubAppManifest("https://agent.example.com", "tyrs-hand-a1b2c3d4")
	require.Equal(t, "tyrs-hand-a1b2c3d4", manifest["name"])
	require.Equal(t, "https://agent.example.com", manifest["url"])
	require.Equal(t, "https://agent.example.com/api/v1/github/app/manifest/callback", manifest["redirect_url"])
	require.Equal(t, map[string]any{
		"url": "https://agent.example.com/webhooks/github", "active": true,
	}, manifest["hook_attributes"])

	events := manifest["default_events"].([]string)
	require.NotContains(t, events, "installation")
	require.NotContains(t, events, "installation_repositories")
	require.Contains(t, events, "issues")
	require.Contains(t, events, "pull_request")
}
