package hostworker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppServerEnvironmentOnlyUsesHomeForCodexConfiguration(t *testing.T) {
	environment := appServerEnvironment([]string{
		"PATH=/usr/bin", "LANG=zh_CN.UTF-8", "TYRS_HAND_WORKER_CONTROL_URL=https://control",
		"TYRS_HAND_WORKER_ENROLLMENT_TOKEN=secret", "OPENAI_API_KEY=model-secret",
		"OPENAI_BASE_URL=https://provider.example/v1", "HTTPS_PROXY=https://proxy.example",
		"CUSTOM_TOOL_HOME=/opt/custom",
	})
	require.ElementsMatch(t, []string{"PATH=/usr/bin", "LANG=zh_CN.UTF-8",
		"CUSTOM_TOOL_HOME=/opt/custom"}, environment)
}

func TestReplaceEnvironmentInjectsManagedBrowserToken(t *testing.T) {
	environment := replaceEnvironment([]string{
		"PATH=/usr/bin", "TYRS_BROWSER_MCP_TOKEN=stale",
	}, map[string]string{"TYRS_BROWSER_MCP_TOKEN": "derived"})
	require.ElementsMatch(t, []string{
		"PATH=/usr/bin", "TYRS_BROWSER_MCP_TOKEN=derived",
	}, environment)
}
