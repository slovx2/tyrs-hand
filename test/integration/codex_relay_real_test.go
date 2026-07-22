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
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexrelay"
	"github.com/stretchr/testify/require"
)

func TestRealCodexRelayDesktopAndWorkerDynamicToolRoundTrip(t *testing.T) {
	bin := fixedCodexBinary(t)
	var responseNumber atomic.Int32
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/v1/responses", request.URL.Path)
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		if responseNumber.Add(1) == 1 {
			_, _ = fmt.Fprint(response, sse(
				map[string]any{"type": "response.created", "response": map[string]any{"id": "relay-resp-1"}},
				map[string]any{"type": "response.output_item.done", "item": map[string]any{
					"type": "function_call", "call_id": "relay-call-1", "namespace": "github",
					"name": "echo", "arguments": `{"message":"relay"}`,
				}}, completedResponse("relay-resp-1")))
			return
		}
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": "relay-resp-2"}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "relay-message-1",
				"content": []map[string]any{{"type": "output_text", "text": "relay done"}},
			}}, completedResponse("relay-resp-2")))
	}))
	t.Cleanup(responses.Close)

	root := temporaryDir(t, "tyrs-real-relay-")
	home, workspace := filepath.Join(root, "codex-home"), filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(fmt.Sprintf(`
model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "Relay mock provider"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`, responses.URL+"/v1")), 0o600))

	appSocket := filepath.Join(root, "app.sock")
	process := exec.Command(bin, "app-server", "--listen", "unix://"+appSocket)
	process.Dir = workspace
	process.Env = append(os.Environ(), "CODEX_HOME="+home, "HOME="+root, "RUST_LOG=warn")
	require.NoError(t, process.Start())
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	})
	waitForUnixSocket(t, appSocket)

	relaySocket := filepath.Join(root, "relay.sock")
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: relaySocket, UpstreamSocketPath: appSocket,
		Controller: codexrelay.PassThroughController{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })

	toolCalled := make(chan codex.ServerRequest, 1)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker,
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			toolCalled <- request
			return codex.TextToolResult("relay-tool-ok", true), nil
		}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: relaySocket, RequestTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = desktop.Close() })

	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	require.NoError(t, desktop.Call(context.Background(), "thread/start", map[string]any{
		"cwd": workspace, "model": "mock-model", "approvalPolicy": "never", "sandbox": "read-only",
		"dynamicTools": []map[string]any{{"type": "namespace", "name": "github",
			"description": "Relay tools", "tools": []map[string]any{{
				"type": "function", "name": "echo", "description": "Echo",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
					"message": map[string]string{"type": "string"}}, "required": []string{"message"},
					"additionalProperties": false},
			}}}},
	}, &started))
	require.NotEmpty(t, started.Thread.ID)
	workerEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: started.Thread.ID})
	t.Cleanup(workerEvents.Close)
	desktopEvents := desktop.Subscribe(codex.ThreadFilter{ThreadID: started.Thread.ID})
	t.Cleanup(desktopEvents.Close)

	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, desktop.Call(context.Background(), "turn/start", map[string]any{
		"threadId": started.Thread.ID,
		"input":    []map[string]any{{"type": "text", "text": "Call the echo tool.", "textElements": []any{}}},
	}, &turn))
	require.NotEmpty(t, turn.Turn.ID)
	select {
	case request := <-toolCalled:
		require.Equal(t, "item/tool/call", request.Method)
	case <-time.After(10 * time.Second):
		t.Fatal("真实 Codex 的动态工具请求没有路由到 Worker")
	}
	waitForRelayTurnCompleted(t, desktopEvents.Events(), started.Thread.ID, turn.Turn.ID)
	waitForRelayTurnCompleted(t, workerEvents.Events(), started.Thread.ID, turn.Turn.ID)
	require.Equal(t, int64(1), relay.Stats().UpstreamConnections)
	require.Equal(t, int64(1), relay.Stats().UpstreamInitializations)
}

func waitForUnixSocket(t *testing.T, path string) {
	t.Helper()
	require.Eventually(t, func() bool {
		info, err := os.Stat(path)
		return err == nil && info.Mode()&os.ModeSocket != 0
	}, 10*time.Second, 20*time.Millisecond)
}

func waitForRelayTurnCompleted(t *testing.T, events <-chan codex.Event, threadID, turnID string) {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Method != "turn/completed" {
				continue
			}
			var params struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			if params.ThreadID == threadID && params.Turn.ID == turnID {
				return
			}
		case <-timer.C:
			t.Fatal("等待 Relay Turn完成超时")
		}
	}
}
