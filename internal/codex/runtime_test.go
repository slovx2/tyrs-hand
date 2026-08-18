package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
)

type recordingRuntimeClient struct {
	method  string
	payload map[string]any
}

func (c *recordingRuntimeClient) Call(_ context.Context, method string, payload, result any) error {
	c.method = method
	c.payload = payload.(map[string]any)
	if result == nil {
		return nil
	}
	return json.Unmarshal([]byte(`{"turn":{"id":"turn-1"}}`), result)
}

type eventRuntimeClient struct {
	recordingRuntimeClient
	events chan Event
}

func (c *eventRuntimeClient) Events() <-chan Event { return c.events }

type configRuntimeClient struct {
	payload map[string]any
}

func (c *configRuntimeClient) Call(_ context.Context, method string, payload, result any) error {
	c.payload = payload.(map[string]any)
	if method != "config/read" {
		return nil
	}
	return json.Unmarshal([]byte(`{"config":{"model_provider":"default-provider","model_providers":{"thread-provider":{"base_url":"https://api.example.com/v1","env_key":"PROVIDER_KEY","http_headers":{"X-Static":"one"},"env_http_headers":{"X-Env":"HEADER_KEY"},"query_params":{"tenant":"a"}}}}}`), result)
}

func TestStartTurnCarriesCollaborationMode(t *testing.T) {
	for _, mode := range []string{"default", "plan"} {
		t.Run(mode, func(t *testing.T) {
			client := &recordingRuntimeClient{}
			turnID, err := NewRuntime(client).StartTurn(context.Background(), "thread-1",
				ports.TurnInput{Text: "test", CollaborationMode: &ports.CollaborationMode{
					Mode: mode, Model: "gpt-5.6-sol", ReasoningEffort: "high",
				}})
			require.NoError(t, err)
			require.Equal(t, "turn-1", turnID)
			require.Equal(t, "turn/start", client.method)
			collaboration := client.payload["collaborationMode"].(map[string]any)
			require.Equal(t, mode, collaboration["mode"])
			settings := collaboration["settings"].(map[string]any)
			require.Equal(t, "gpt-5.6-sol", settings["model"])
			require.Equal(t, "high", settings["reasoning_effort"])
		})
	}
}

func TestStartTurnCarriesCollaborationModeWithoutExplicitModel(t *testing.T) {
	client := &recordingRuntimeClient{}
	_, err := NewRuntime(client).StartTurn(context.Background(), "thread-1",
		ports.TurnInput{Text: "test", CollaborationMode: &ports.CollaborationMode{Mode: "default"}})
	require.NoError(t, err)
	collaboration := client.payload["collaborationMode"].(map[string]any)
	require.Equal(t, "default", collaboration["mode"])
	require.Equal(t, "", collaboration["settings"].(map[string]any)["model"])
}

func TestRollbackThreadUsesSingleLatestTurn(t *testing.T) {
	client := &recordingRuntimeClient{}
	err := NewRuntime(client).RollbackThread(context.Background(), "thread-1", 1)
	require.NoError(t, err)
	require.Equal(t, "thread/rollback", client.method)
	require.Equal(t, "thread-1", client.payload["threadId"])
	require.Equal(t, 1, client.payload["numTurns"])
	require.ErrorContains(t, NewRuntime(client).RollbackThread(context.Background(), "thread-1", 2),
		"只允许 rollback 最新一个 turn")
}

func TestResumeThreadDoesNotMigrateDynamicTools(t *testing.T) {
	client := &recordingRuntimeClient{}
	err := NewRuntime(client).ResumeThread(context.Background(), "thread-1", ports.ThreadOptions{
		CWD: t.TempDir(), DynamicTools: []ports.DynamicToolSpec{{Type: "function", Name: "new-tool"}},
	})
	require.NoError(t, err)
	require.Equal(t, "thread/resume", client.method)
	require.NotContains(t, client.payload, "dynamicTools")
}

func TestReadRuntimeConfigReturnsMergedProviderFields(t *testing.T) {
	client := &configRuntimeClient{}
	config, err := NewRuntime(client).ReadRuntimeConfig(context.Background(), t.TempDir())
	require.NoError(t, err)
	require.Equal(t, false, client.payload["includeLayers"])
	require.Equal(t, "default-provider", config.ModelProvider)
	provider := config.ModelProviders["thread-provider"]
	require.Equal(t, "https://api.example.com/v1", provider.BaseURL)
	require.Equal(t, "PROVIDER_KEY", provider.EnvKey)
	require.Equal(t, "one", provider.HTTPHeaders["X-Static"])
	require.Equal(t, "HEADER_KEY", provider.EnvHTTPHeaders["X-Env"])
	require.Equal(t, "a", provider.QueryParams["tenant"])
}

func TestThreadPayloadAndSkillInput(t *testing.T) {
	root := t.TempDir()
	payload := threadPayload(ports.ThreadOptions{
		CWD: root, Model: "model", ReasoningEffort: "high", ServiceTier: "priority",
		Sandbox: "workspace-write", ApprovalPolicy: "never", NetworkEnabled: true,
		DeveloperInstructions: "instructions",
		RuntimeConfig: map[string]any{
			"features": map[string]any{"custom": true},
			"hooks":    map[string]any{"Stop": []any{"hook"}},
			"sandbox_workspace_write": map[string]any{
				"writable_roots": []string{"/data/worker/caches", "/data/worker/state"},
			},
		},
	})
	require.Equal(t, "model", payload["model"])
	require.NotContains(t, payload, "effort")
	require.NotContains(t, payload, "serviceTier")
	require.Equal(t, filepath.Clean(root), payload["cwd"])
	config := payload["config"].(map[string]any)
	require.Equal(t, "high", config["model_reasoning_effort"])
	require.Equal(t, "priority", config["service_tier"])
	require.Equal(t, false, config["features"].(map[string]any)["memories"])
	require.Equal(t, true, config["features"].(map[string]any)["custom"])
	require.Equal(t, []any{"hook"}, config["hooks"].(map[string]any)["Stop"])
	sandbox := config["sandbox_workspace_write"].(map[string]any)
	require.Equal(t, true, sandbox["network_access"])
	require.Equal(t, []string{"/data/worker/caches", "/data/worker/state"}, sandbox["writable_roots"])

	skill := ports.SkillRef{Name: "review", Path: filepath.Join(root, "SKILL.md")}
	items := userInput(ports.TurnInput{Text: "inspect", Skills: []ports.SkillRef{skill}})
	require.Equal(t, "$review\ninspect", items[0]["text"])
	require.Equal(t, "skill", items[1]["type"])
	require.Equal(t, "review", items[1]["name"])

	image := filepath.Join(root, "image.png")
	participant := participantidentity.Participant{
		ID: participantidentity.ID("guild", "user"), DisplayName: "Alice",
	}
	input := ports.TurnInput{
		Text: "look", LocalImages: []ports.LocalImageInput{{Path: image, Detail: "high"}},
		AdditionalContext: participantidentity.AdditionalContext(participant),
	}
	items = userInput(input)
	require.Equal(t, "localImage", items[1]["type"])
	require.Equal(t, filepath.Clean(image), items[1]["path"])
	payload = map[string]any{}
	addTurnContext(payload, input.AdditionalContext)
	context := payload["additionalContext"].(map[string]map[string]string)
	require.Equal(t, "application", context[participantidentity.IdentityContextKey]["kind"])
	require.Equal(t, "untrusted", context[participantidentity.ProfileContextKey]["kind"])
}

func TestRuntimeSnapshotHelpersAndForwarding(t *testing.T) {
	closedEvents := NewRuntime(&recordingRuntimeClient{}).Events()
	_, open := <-closedEvents
	require.False(t, open)

	eventClient := &eventRuntimeClient{events: make(chan Event, 1)}
	eventClient.events <- Event{Method: "turn/completed"}
	require.Equal(t, "turn/completed", (<-NewRuntime(eventClient).Events()).Method)

	require.Equal(t, "idle", (ThreadSnapshot{Status: json.RawMessage(`{"type":"idle"}`)}).StatusType())
	require.Empty(t, (ThreadSnapshot{Status: json.RawMessage(`invalid`)}).StatusType())

	final := TurnSnapshot{Items: []ItemSnapshot{
		{Type: "agentMessage", Text: "progress"},
		{Type: "agentMessage", Phase: "final_answer", Text: "done"},
	}}
	require.Equal(t, "done", final.FinalAnswer())
	require.Equal(t, "plan body", (TurnSnapshot{Items: append(final.Items,
		ItemSnapshot{Type: "plan", Text: "plan body"})}).FinalAnswer())
	require.Equal(t, "progress", (TurnSnapshot{Items: final.Items[:1]}).FinalAnswer())
	require.Empty(t, (TurnSnapshot{Items: []ItemSnapshot{{Type: "userMessage", Text: "hello"}}}).FinalAnswer())

	snapshot := ThreadSnapshot{Turns: []TurnSnapshot{{ID: "turn-1", Status: "completed"}}}
	_, found := snapshot.TurnByID("missing")
	require.False(t, found)
	_, active := snapshot.ActiveTurn()
	require.False(t, active)

	client := &recordingRuntimeClient{}
	require.NoError(t, NewRuntime(client).SetThreadName(context.Background(), "thread-1", "New name"))
	require.Equal(t, "thread/name/set", client.method)
	require.Equal(t, "thread-1", client.payload["threadId"])
	require.Equal(t, "New name", client.payload["name"])
}

func TestPoolRoutesByThreadAndReleasesJobProcess(t *testing.T) {
	pool := NewPool(PoolOptions{Bin: os.Args[0], RequestTimeout: 2 * time.Second, ToolTimeout: 2 * time.Second})
	t.Cleanup(func() { _ = pool.Close() })
	cwd := t.TempDir()
	home := t.TempDir()
	client, err := pool.Acquire(context.Background(), "repo/profile/config", cwd, home, []string{"GO_WANT_FAKE_CODEX=1"})
	require.NoError(t, err)
	again, err := pool.Acquire(context.Background(), "repo/profile/config", cwd, home, nil)
	require.NoError(t, err)
	require.Same(t, client, again)
	runtime := NewRuntime(client)
	threadID, err := runtime.StartThread(context.Background(), ports.ThreadOptions{CWD: cwd, Sandbox: "workspace-write", ApprovalPolicy: "never"})
	require.NoError(t, err)
	calls := make(chan ToolCallRequest, 1)
	unbind, err := pool.Bind("repo/profile/config", threadID, func(_ context.Context, request ToolCallRequest) (ToolCallResult, error) {
		calls <- request
		return TextToolResult("ok", true), nil
	})
	require.NoError(t, err)
	_, err = runtime.StartTurn(context.Background(), threadID, ports.TurnInput{Text: "test"})
	require.NoError(t, err)
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("进程池没有路由 Tool Call")
	}
	unbind()
	_, err = pool.routeTool("repo/profile/config", context.Background(), ToolCallRequest{ThreadID: threadID, Arguments: json.RawMessage(`{}`)})
	require.Error(t, err)
	require.NoError(t, pool.Release("repo/profile/config"))
	select {
	case <-client.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Release 没有关闭 Job 的 App Server")
	}
	replacement, err := pool.Acquire(context.Background(), "repo/profile/config", cwd, home, []string{"GO_WANT_FAKE_CODEX=1"})
	require.NoError(t, err)
	require.NotSame(t, client, replacement)
	require.NoError(t, pool.Close())
	_, err = pool.Acquire(context.Background(), "new", cwd, home, nil)
	require.Error(t, err)
}

func TestPoolJobProcessesAreIsolated(t *testing.T) {
	pool := NewPool(PoolOptions{Bin: os.Args[0], RequestTimeout: 2 * time.Second, ToolTimeout: 2 * time.Second})
	t.Cleanup(func() { _ = pool.Close() })
	cwd := t.TempDir()
	first, err := pool.Acquire(context.Background(), "job/first", cwd, t.TempDir(), []string{"GO_WANT_FAKE_CODEX=1"})
	require.NoError(t, err)
	second, err := pool.Acquire(context.Background(), "job/second", cwd, t.TempDir(), []string{"GO_WANT_FAKE_CODEX=1"})
	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.NoError(t, pool.Release("job/first"))
	select {
	case <-second.Done():
		t.Fatal("关闭一个 Job 的 App Server 不应影响其他 Job")
	default:
	}
	_, err = NewRuntime(second).StartThread(context.Background(), ports.ThreadOptions{
		CWD: cwd, Sandbox: "workspace-write", ApprovalPolicy: "never",
	})
	require.NoError(t, err)
}

func TestValidateRepositorySkills(t *testing.T) {
	cwd := t.TempDir()
	skillPath := filepath.Join(cwd, ".agents", "skills", "demo", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o755))
	require.NoError(t, os.WriteFile(skillPath, []byte("skill"), 0o600))
	client, err := Start(context.Background(), ClientOptions{
		Bin: os.Args[0], CWD: cwd, CodexHome: t.TempDir(), RequestTimeout: 2 * time.Second,
		Environment: []string{"GO_WANT_FAKE_CODEX=1", "FAKE_CODEX_MODE=skills", "FAKE_SKILL_PATH=" + skillPath},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	_ = client.Events()
	runtime := NewRuntime(client)
	require.NoError(t, runtime.ValidateSkills(context.Background(), cwd, []ports.SkillRef{{Name: "demo", Path: skillPath}}))
	require.Error(t, runtime.ValidateSkills(context.Background(), cwd, []ports.SkillRef{{Name: "missing", Path: skillPath}}))
}
