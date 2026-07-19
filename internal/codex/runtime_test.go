package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestThreadPayloadAndSkillInput(t *testing.T) {
	root := t.TempDir()
	payload := threadPayload(ports.ThreadOptions{
		CWD: root, Model: "model", ReasoningEffort: "high", ServiceTier: "priority",
		Sandbox: "workspace-write", ApprovalPolicy: "never", NetworkEnabled: true,
		DeveloperInstructions: "instructions",
	})
	require.Equal(t, "model", payload["model"])
	require.Equal(t, "high", payload["effort"])
	require.Equal(t, filepath.Clean(root), payload["cwd"])
	config := payload["config"].(map[string]any)
	require.Equal(t, false, config["features"].(map[string]any)["memory_tool"])

	skill := ports.SkillRef{Name: "review", Path: filepath.Join(root, "SKILL.md")}
	items := userInput(ports.TurnInput{Text: "inspect", Skills: []ports.SkillRef{skill}})
	require.Equal(t, "$review\ninspect", items[0]["text"])
	require.Equal(t, "skill", items[1]["type"])
	require.Equal(t, "review", items[1]["name"])

	image := filepath.Join(root, "image.png")
	input := ports.TurnInput{
		Text: "look", LocalImages: []ports.LocalImageInput{{Path: image, Detail: "high"}},
		AdditionalContext: map[string]ports.AdditionalContextEntry{
			"discord_message_identity": {Kind: "application", Value: `{"message_id":"1"}`},
		},
	}
	items = userInput(input)
	require.Equal(t, "localImage", items[1]["type"])
	require.Equal(t, filepath.Clean(image), items[1]["path"])
	payload = map[string]any{}
	addTurnContext(payload, input.AdditionalContext)
	context := payload["additionalContext"].(map[string]map[string]string)
	require.Equal(t, "application", context["discord_message_identity"]["kind"])
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
