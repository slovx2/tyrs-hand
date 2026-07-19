//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/replygate"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRealCodexStopHookBlocksThreeTimes(t *testing.T) {
	bin := fixedCodexBinary(t)
	var requestCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		response.Header().Set("Content-Type", "text/event-stream")
		id := fmt.Sprintf("response-%d", requestCount.Add(1))
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": id}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "message-" + id,
				"content": []map[string]any{{"type": "output_text", "text": "natural answer without reply tool"}},
			}}, completedResponse(id)))
	}))
	t.Cleanup(upstream.Close)

	root := temporaryDir(t, "tyrs-hand-codex-hook-")
	hookBin := filepath.Join(root, replygate.HookCommand)
	build := exec.Command("go", "build", "-o", hookBin, "./cmd/tyrs-hand-reply-hook")
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	build.Dir = filepath.Join(filepath.Dir(source), "..", "..")
	require.NoError(t, build.Run())
	codexHome := filepath.Join(root, "codex-home")
	worktree := filepath.Join(root, "worktree")
	require.NoError(t, os.MkdirAll(codexHome, 0o700))
	require.NoError(t, os.MkdirAll(worktree, 0o700))
	config := fmt.Sprintf(`model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "Mock provider"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`, upstream.URL+"/v1")
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o600))
	require.NoError(t, replygate.Install(codexHome))
	client, err := codex.Start(context.Background(), codex.ClientOptions{
		Bin: bin, CWD: worktree, CodexHome: codexHome,
		Environment:    []string{"PATH=" + root + string(os.PathListSeparator) + os.Getenv("PATH")},
		RequestTimeout: 30 * time.Second, Logger: zap.NewNop(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	runtimeClient := codex.NewRuntime(client)
	threadID, err := runtimeClient.StartThread(context.Background(), ports.ThreadOptions{
		CWD: worktree, Model: "mock-model", Sandbox: "read-only", ApprovalPolicy: "never",
		RuntimeConfig: replygate.SessionConfig(),
	})
	require.NoError(t, err)
	require.NoError(t, replygate.Initialize(codexHome, threadID, "intent-hook", true, 3))
	turnID, err := runtimeClient.StartTurn(context.Background(), threadID, ports.TurnInput{Text: "Finish now."})
	require.NoError(t, err)
	waitForCompletedTurn(t, client.Events(), threadID, turnID)
	state, err := replygate.Read(codexHome, threadID)
	require.NoError(t, err)
	require.Equal(t, 4, state.BlockCount)
	require.GreaterOrEqual(t, requestCount.Load(), int32(4))
}
