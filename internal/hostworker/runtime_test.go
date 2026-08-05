package hostworker

import (
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func TestAppServerEnvironmentOnlyUsesHomeForCodexConfiguration(t *testing.T) {
	environment := appServerEnvironment([]string{
		"PATH=/usr/bin", "LANG=zh_CN.UTF-8", "TYRS_HAND_WORKER_CONTROL_URL=https://control",
		"TYRS_HAND_WORKER_ENROLLMENT_TOKEN=secret", "OPENAI_API_KEY=model-secret",
		"OPENAI_BASE_URL=https://provider.example/v1", "HTTPS_PROXY=https://proxy.example",
		"CUSTOM_TOOL_HOME=/opt/custom", codex.BrowserMCPWorkerTokenEnvironment + "=stale",
		codex.BrowserMCPDesktopTokenEnvironment + "=stale",
	})
	require.ElementsMatch(t, []string{"PATH=/usr/bin", "LANG=zh_CN.UTF-8",
		"CUSTOM_TOOL_HOME=/opt/custom"}, environment)
}

func TestReplaceEnvironmentInjectsManagedBrowserToken(t *testing.T) {
	environment := replaceEnvironment([]string{
		"PATH=/usr/bin", codex.BrowserMCPWorkerTokenEnvironment + "=stale",
	}, map[string]string{codex.BrowserMCPWorkerTokenEnvironment: "derived"})
	require.ElementsMatch(t, []string{
		"PATH=/usr/bin", codex.BrowserMCPWorkerTokenEnvironment + "=derived",
	}, environment)
}

func TestOpenEphemeralClientRequiresRunningHub(t *testing.T) {
	var missing *Runtime
	client, err := missing.OpenEphemeralClient()
	require.Nil(t, client)
	require.ErrorContains(t, err, "Runtime 不可用")

	client, err = (&Runtime{}).OpenEphemeralClient()
	require.Nil(t, client)
	require.ErrorContains(t, err, "AppServerHub 尚未启动")
}
