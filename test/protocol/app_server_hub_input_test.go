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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func TestRealCodexHubRequestUserInputDesktopWins(t *testing.T) {
	runRealCodexHubRequestUserInputWinner(t, appserverhub.RoleDesktop)
}

func TestRealCodexHubRequestUserInputWorkerWins(t *testing.T) {
	runRealCodexHubRequestUserInputWinner(t, appserverhub.RoleWorker)
}

func runRealCodexHubRequestUserInputWinner(t *testing.T, winner appserverhub.Role) {
	toolArguments, err := json.Marshal(map[string]any{
		"questions": []map[string]any{{"id": "confirm", "header": "Confirm",
			"question": "Continue?", "options": []map[string]string{{
				"label": "Yes (Recommended)", "description": "Continue."}, {
				"label": "No", "description": "Stop."}}}},
		"autoResolutionMs": 60_000,
	})
	require.NoError(t, err)
	var responseNumber atomic.Int32
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		if responseNumber.Add(1) == 1 {
			_, _ = fmt.Fprint(response, sse(
				map[string]any{"type": "response.created", "response": map[string]any{"id": "input-resp-1"}},
				map[string]any{"type": "response.output_item.done", "item": map[string]any{
					"type": "function_call", "call_id": "input-call-1", "name": "request_user_input",
					"arguments": string(toolArguments),
				}}, completedResponse("input-resp-1")))
			return
		}
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": "input-resp-2"}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "input-message",
				"content": []map[string]any{{"type": "output_text", "text": "input done"}},
			}}, completedResponse("input-resp-2")))
	}))
	t.Cleanup(responses.Close)

	bin := fixedCodexBinary(t)
	root := temporaryDir(t, "tyrs-real-hub-input-")
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	config := fmt.Sprintf(`model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[features]
default_mode_request_user_input = true

[model_providers.mock_provider]
name = "Hub input mock"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`, responses.URL+"/v1")
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600))
	appSocket := filepath.Join(root, "app.sock")
	process := exec.Command(bin, "app-server", "--listen", "unix://"+appSocket)
	process.Dir = workspace
	process.Env = append(os.Environ(), "CODEX_HOME="+home, "HOME="+root, "RUST_LOG=warn")
	require.NoError(t, process.Start())
	t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	waitForUnixSocket(t, appSocket)
	hub, err := appserverhub.Start(context.Background(), appserverhub.Options{
		SocketPath: filepath.Join(root, "hub.sock"), UpstreamSocketPath: appSocket,
		Controller: appserverhub.PassThroughController{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, hub.Close()) })

	workerRequest := make(chan codex.ServerRequest, 1)
	worker, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleWorker,
		ServerRequestHandler: func(ctx context.Context, request codex.ServerRequest) (any, error) {
			workerRequest <- request
			if winner == appserverhub.RoleWorker {
				return inputAnswer(), nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktopRequest := make(chan codex.ServerRequest, 1)
	desktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: hub.SocketPath(), ServerRequestTimeout: 30 * time.Second,
		ServerRequestHandler: func(ctx context.Context, request codex.ServerRequest) (any, error) {
			desktopRequest <- request
			if winner == appserverhub.RoleDesktop {
				return inputAnswer(), nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = desktop.Close() })

	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	require.NoError(t, desktop.Call(context.Background(), "thread/start", map[string]any{
		"cwd": workspace, "model": "mock-model", "approvalPolicy": "never", "sandbox": "read-only",
	}, &thread))
	subscription := desktop.Subscribe(codex.ThreadFilter{ThreadID: thread.Thread.ID})
	t.Cleanup(subscription.Close)
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, desktop.Call(context.Background(), "turn/start", map[string]any{
		"threadId": thread.Thread.ID,
		"input":    []map[string]any{{"type": "text", "text": "Ask me.", "textElements": []any{}}},
		"collaborationMode": map[string]any{"mode": "plan", "settings": map[string]any{
			"model": "mock-model", "reasoning_effort": "medium"}},
	}, &turn))
	waitForHubCollaborationMode(t, subscription.Events(), thread.Thread.ID, "plan")
	select {
	case request := <-desktopRequest:
		require.False(t, strings.Contains(string(request.Params), "autoResolutionMs"))
	case <-time.After(10 * time.Second):
		t.Fatal("Desktop没有收到真实 requestUserInput")
	}
	select {
	case request := <-workerRequest:
		require.Contains(t, string(request.Params), "autoResolutionMs")
	case <-time.After(10 * time.Second):
		t.Fatal("Worker没有同时收到真实 requestUserInput")
	}
	waitForHubTurnCompleted(t, subscription.Events(), thread.Thread.ID, turn.Turn.ID)
	var defaultTurn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, desktop.Call(context.Background(), "turn/start", map[string]any{
		"threadId": thread.Thread.ID,
		"input":    []map[string]any{{"type": "text", "text": "Continue.", "textElements": []any{}}},
		"collaborationMode": map[string]any{"mode": "default", "settings": map[string]any{
			"model": "mock-model", "reasoning_effort": "medium"}},
	}, &defaultTurn))
	waitForHubCollaborationMode(t, subscription.Events(), thread.Thread.ID, "default")
	waitForHubTurnCompleted(t, subscription.Events(), thread.Thread.ID, defaultTurn.Turn.ID)
}

func waitForHubCollaborationMode(t *testing.T, events <-chan codex.Event, threadID, mode string) {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Method != "thread/settings/updated" {
				continue
			}
			var value struct {
				ThreadID       string `json:"threadId"`
				ThreadSettings struct {
					CollaborationMode struct {
						Mode string `json:"mode"`
					} `json:"collaborationMode"`
				} `json:"threadSettings"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &value))
			if value.ThreadID == threadID && value.ThreadSettings.CollaborationMode.Mode == mode {
				return
			}
		case <-timer.C:
			t.Fatalf("等待 collaboration mode %s 超时", mode)
		}
	}
}

func inputAnswer() map[string]any {
	return map[string]any{"answers": map[string]any{"confirm": map[string]any{
		"answers": []string{"yes"}}}}
}
