package hostworker

import (
	"context"
	"sync"
	"testing"

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

func TestDesktopFailureRestartsStoppedAppServerOnlyOnce(t *testing.T) {
	failed := &appServerGeneration{done: make(chan struct{}), waitErr: context.Canceled,
		generation: 1}
	close(failed.done)
	next := &appServerGeneration{done: make(chan struct{}), generation: 2}
	runtime := &Runtime{current: failed, options: RuntimeOptions{Logger: zap.NewNop()}}
	started := 0
	runtime.start = func(context.Context) (*appServerGeneration, error) {
		started++
		return next, nil
	}

	var wait sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- runtime.recoverAfterDesktopFailure(context.Background(), failed)
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, 1, started)
	require.Equal(t, next, runtime.current)
}

func TestDesktopDisconnectDoesNotProbeOrRestartRunningAppServer(t *testing.T) {
	running := &appServerGeneration{done: make(chan struct{}), generation: 1}
	runtime := &Runtime{current: running, options: RuntimeOptions{Logger: zap.NewNop()}}
	runtime.start = func(context.Context) (*appServerGeneration, error) {
		t.Fatal("运行中的 App Server 不应被探测或重启")
		return nil, nil
	}
	require.NoError(t, runtime.recoverAfterDesktopFailure(context.Background(), running))
}

func TestRestartReplacesRunningAppServerGeneration(t *testing.T) {
	current := &appServerGeneration{done: make(chan struct{}), generation: 1}
	next := &appServerGeneration{done: make(chan struct{}), generation: 2}
	runtime := &Runtime{current: current, options: RuntimeOptions{Logger: zap.NewNop()}}
	started := 0
	runtime.start = func(context.Context) (*appServerGeneration, error) {
		started++
		return next, nil
	}

	require.NoError(t, runtime.Restart())
	require.Equal(t, 1, started)
	require.Equal(t, next, runtime.current)
}

func TestRestartRejectsStoppedAppServer(t *testing.T) {
	stopped := &appServerGeneration{done: make(chan struct{}), generation: 1}
	close(stopped.done)
	runtime := &Runtime{current: stopped, options: RuntimeOptions{Logger: zap.NewNop()}}
	runtime.start = func(context.Context) (*appServerGeneration, error) {
		t.Fatal("已停止的 App Server 不应进入受控重启")
		return nil, nil
	}

	require.ErrorContains(t, runtime.Restart(), "App Server 不可用")
}

func TestAppServerGenerationOutlivesRecoveryStartupContext(t *testing.T) {
	startup, cancel := context.WithCancel(context.Background())
	lifetime := appServerGenerationContext(startup)
	cancel()

	require.ErrorIs(t, startup.Err(), context.Canceled)
	require.NoError(t, lifetime.Err())
}
