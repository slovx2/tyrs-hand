package hostworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestAppServerEnvironmentKeepsOpenAIProviderForAgentTools(t *testing.T) {
	environment := appServerEnvironment([]string{
		"PATH=/usr/bin", "LANG=zh_CN.UTF-8", "TYRS_HAND_WORKER_CONTROL_URL=https://control",
		"TYRS_HAND_WORKER_ENROLLMENT_TOKEN=secret", "OPENAI_API_KEY=model-secret",
		"OPENAI_BASE_URL=https://provider.example/v1", "HTTPS_PROXY=https://proxy.example",
		"CUSTOM_TOOL_HOME=/opt/custom", codex.BrowserMCPWorkerTokenEnvironment + "=stale",
		codex.BrowserMCPDesktopTokenEnvironment + "=stale",
	})
	require.ElementsMatch(t, []string{"PATH=/usr/bin", "LANG=zh_CN.UTF-8",
		"OPENAI_API_KEY=model-secret", "OPENAI_BASE_URL=https://provider.example/v1",
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

func TestRuntimeClientUnavailableWhileRestarting(t *testing.T) {
	runtime := &Runtime{status: "restarting", generation: 12,
		current: &runtimeGeneration{client: &appserverhub.Client{}}}
	client, generation := runtime.ClientSnapshot()
	require.Nil(t, client)
	require.EqualValues(t, 12, generation)
	require.Nil(t, runtime.Client())
}

func TestRuntimeRestartsAppServerWithoutStoppingManager(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	first := &runtimeGeneration{done: make(chan struct{})}
	second := &runtimeGeneration{done: make(chan struct{})}
	started := make(chan struct{}, 1)
	runtime := &Runtime{ctx: ctx, cancel: cancel, current: first, generation: 1,
		status: "running", done: make(chan struct{}), generationChanged: make(chan int64, 1),
		options: RuntimeOptions{Logger: zap.NewNop()}}
	runtime.start = func() (*runtimeGeneration, error) {
		started <- struct{}{}
		return second, nil
	}
	go runtime.supervise(first)

	first.waitErr = errors.New("app-server stopped")
	close(first.done)
	select {
	case <-runtime.Done():
		t.Fatal("App Server 退出不应停止 Runtime Manager")
	case <-started:
	}
	require.Eventually(t, func() bool {
		return runtime.Status() == "running" && runtime.Generation() > 1
	}, time.Second, 10*time.Millisecond)
	require.ErrorContains(t, runtime.Err(), "app-server stopped")

	cancel()
	require.Eventually(t, func() bool {
		select {
		case <-runtime.Done():
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
}
