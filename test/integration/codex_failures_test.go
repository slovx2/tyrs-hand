//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRealCodexHandlesMockUpstreamFailures(t *testing.T) {
	bin := fixedCodexBinary(t)
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "429",
			handler: func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(http.StatusTooManyRequests)
				_, _ = fmt.Fprint(response, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			},
		},
		{
			name: "sse-error",
			handler: func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(response, "event: error\ndata: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"mock error\"}\n\n")
			},
		},
		{
			name: "disconnected-stream",
			handler: func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(response, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"partial\"}}\n\n")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(test.handler)
			defer upstream.Close()
			client, runtime, cwd := realCodexRuntime(t, bin, upstream.URL)
			defer client.Close()
			threadID, err := runtime.StartThread(context.Background(), ports.ThreadOptions{CWD: cwd, Model: "mock-model", Sandbox: "read-only", ApprovalPolicy: "never"})
			require.NoError(t, err)
			turnID, err := runtime.StartTurn(context.Background(), threadID, ports.TurnInput{Text: "test failure"})
			require.NoError(t, err)
			status := waitForTurnStatus(t, client.Events(), threadID, turnID)
			require.NotEqual(t, "completed", status)
		})
	}
}

func TestRealCodexResumesThreadAfterProcessRestart(t *testing.T) {
	bin := fixedCodexBinary(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": "response"}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "role": "assistant", "id": "message", "content": []map[string]any{{"type": "output_text", "text": "done"}}}},
			completedResponse("response"),
		))
	}))
	defer upstream.Close()
	root := temporaryDir(t, "tyrs-hand-codex-resume-")
	home := filepath.Join(root, "home")
	cwd := filepath.Join(root, "worktree")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(cwd, 0o755))
	writeMockCodexConfig(t, home, upstream.URL)
	start := func() (*codex.Client, *codex.Runtime) {
		client, err := codex.Start(context.Background(), codex.ClientOptions{Bin: bin, CWD: cwd, CodexHome: home, RequestTimeout: 30 * time.Second, Logger: zap.NewNop()})
		require.NoError(t, err)
		return client, codex.NewRuntime(client)
	}
	firstClient, firstRuntime := start()
	options := ports.ThreadOptions{CWD: cwd, Model: "mock-model", Sandbox: "read-only", ApprovalPolicy: "never"}
	threadID, err := firstRuntime.StartThread(context.Background(), options)
	require.NoError(t, err)
	turnID, err := firstRuntime.StartTurn(context.Background(), threadID, ports.TurnInput{Text: "first"})
	require.NoError(t, err)
	require.Equal(t, "completed", waitForTurnStatus(t, firstClient.Events(), threadID, turnID))
	require.NoError(t, firstClient.Close())

	secondClient, secondRuntime := start()
	defer secondClient.Close()
	require.NoError(t, secondRuntime.ResumeThread(context.Background(), threadID, options))
}

func TestRealCodexInterruptsBlockedTurn(t *testing.T) {
	bin := fixedCodexBinary(t)
	requestStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		requestStarted <- struct{}{}
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer upstream.Close()
	client, runtime, cwd := realCodexRuntime(t, bin, upstream.URL)
	defer client.Close()
	threadID, err := runtime.StartThread(context.Background(), ports.ThreadOptions{CWD: cwd, Model: "mock-model", Sandbox: "read-only", ApprovalPolicy: "never"})
	require.NoError(t, err)
	turnID, err := runtime.StartTurn(context.Background(), threadID, ports.TurnInput{Text: "block"})
	require.NoError(t, err)
	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Mock 上游没有收到请求")
	}
	require.NoError(t, runtime.InterruptTurn(context.Background(), threadID, turnID))
	close(release)
	require.NotEqual(t, "completed", waitForTurnStatus(t, client.Events(), threadID, turnID))
}

func realCodexRuntime(t *testing.T, bin, upstreamURL string) (*codex.Client, *codex.Runtime, string) {
	t.Helper()
	root := temporaryDir(t, "tyrs-hand-codex-runtime-")
	home := filepath.Join(root, "home")
	cwd := filepath.Join(root, "worktree")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(cwd, 0o755))
	writeMockCodexConfig(t, home, upstreamURL)
	client, err := codex.Start(context.Background(), codex.ClientOptions{
		Bin: bin, CWD: cwd, CodexHome: home, RequestTimeout: 30 * time.Second,
		ToolTimeout: 30 * time.Second, Logger: zap.NewNop(),
	})
	require.NoError(t, err)
	return client, codex.NewRuntime(client), cwd
}

func writeMockCodexConfig(t *testing.T, home, upstreamURL string) {
	t.Helper()
	config := fmt.Sprintf(`model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "Mock provider for integration test"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`, upstreamURL+"/v1")
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600))
}

func waitForTurnStatus(t *testing.T, events <-chan codex.Event, threadID, turnID string) string {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			require.True(t, ok, "App Server 在 Turn 完成前退出")
			if event.Method != "turn/completed" {
				continue
			}
			var value struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"turn"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &value))
			if value.ThreadID == threadID && value.Turn.ID == turnID {
				return value.Turn.Status
			}
		case <-timer.C:
			t.Fatal("等待真实 Codex Turn 状态超时")
		}
	}
}
