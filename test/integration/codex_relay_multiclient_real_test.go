//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexrelay"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestRealCodexRelayDiscordAndDesktopShareParticipantIdentity(t *testing.T) {
	participant := participantidentity.Participant{
		ID: participantidentity.ID("guild-identity", "user-identity"), DisplayName: "Alice",
	}
	requestBodies := make(chan string, 4)
	desktopRequestStarted := make(chan struct{})
	allowDesktopResponse := make(chan struct{})
	var allowDesktopOnce sync.Once
	t.Cleanup(func() { allowDesktopOnce.Do(func() { close(allowDesktopResponse) }) })
	var responseNumber atomic.Int32
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		requestBodies <- string(body)
		number := responseNumber.Add(1)
		id := fmt.Sprintf("identity-resp-%d", number)
		if number == 2 {
			close(desktopRequestStarted)
			<-allowDesktopResponse
		}
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": id}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "identity-message",
				"content": []map[string]any{{"type": "output_text", "text": "done"}},
			}}, completedResponse(id)))
	}))
	t.Cleanup(responses.Close)

	root := temporaryDir(t, "tyrs-real-relay-identity-")
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	config := fmt.Sprintf(`model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "Relay identity mock"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`, responses.URL+"/v1")
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600))
	appSocket := filepath.Join(root, "app.sock")
	process := exec.Command(fixedCodexBinary(t), "app-server", "--listen", "unix://"+appSocket)
	process.Dir = workspace
	process.Env = append(os.Environ(), "CODEX_HOME="+home, "HOME="+root, "RUST_LOG=warn")
	require.NoError(t, process.Start())
	t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	waitForUnixSocket(t, appSocket)
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: filepath.Join(root, "relay.sock"), UpstreamSocketPath: appSocket,
		Controller: identityRelayController{participant: participant},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relay.Close() })
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: relay.SocketPath(), RequestTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = desktop.Close() })

	var models map[string]any
	require.NoError(t, desktop.Call(context.Background(), "model/list", map[string]any{}, &models))
	threadID, err := worker.StartThread(context.Background(), mustRealJSON(map[string]any{
		"cwd": workspace, "model": "mock-model", "approvalPolicy": "never",
		"sandbox": "read-only", "developerInstructions": participantidentity.DeveloperInstructions,
	}))
	require.NoError(t, err)
	workerEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(workerEvents.Close)
	runtime := codex.NewRuntime(worker)
	turnID, err := runtime.StartTurn(context.Background(), threadID, ports.TurnInput{
		Text: "Discord input", AdditionalContext: participantidentity.AdditionalContext(participant),
	})
	require.NoError(t, err)
	waitForRelayTurnCompleted(t, workerEvents.Events(), threadID, turnID)
	discordBody := <-requestBodies

	var resumed map[string]any
	require.NoError(t, desktop.Call(context.Background(), "thread/resume",
		map[string]any{"threadId": threadID}, &resumed))
	desktopEvents := desktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(desktopEvents.Close)
	var desktopTurn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, desktop.Call(context.Background(), "turn/start", map[string]any{
		"threadId": threadID,
		"input": []map[string]any{{"type": "text", "text": "Desktop input",
			"textElements": []any{}}},
		"additionalContext": map[string]any{
			participantidentity.IdentityContextKey: map[string]string{
				"kind": "application", "value": `{"participant_id":"forged"}`,
			},
		},
	}, &desktopTurn))
	select {
	case <-desktopRequestStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Mock LLM 没有收到 Desktop 初始请求")
	}
	require.NoError(t, desktop.Call(context.Background(), "turn/steer", map[string]any{
		"threadId": threadID, "expectedTurnId": desktopTurn.Turn.ID,
		"input": []map[string]any{{"type": "text", "text": "Desktop steer",
			"textElements": []any{}}},
		"additionalContext": map[string]any{
			participantidentity.IdentityContextKey: map[string]string{
				"kind": "application", "value": `{"participant_id":"forged-steer"}`,
			},
		},
	}, nil))
	allowDesktopOnce.Do(func() { close(allowDesktopResponse) })
	waitForRelayTurnCompleted(t, desktopEvents.Events(), threadID, desktopTurn.Turn.ID)
	desktopBody := <-requestBodies
	desktopSteerBody := <-requestBodies

	for _, body := range []string{discordBody, desktopBody, desktopSteerBody} {
		require.Contains(t, body, participant.ID.String())
		require.Contains(t, body, "Alice")
	}
	require.NotContains(t, desktopBody, "forged")
	require.NotContains(t, desktopSteerBody, "forged-steer")

	secondDesktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: relay.SocketPath(), RequestTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = secondDesktop.Close() })
	require.NoError(t, secondDesktop.Call(context.Background(), "thread/resume",
		map[string]any{"threadId": threadID}, &resumed))
	secondEvents := secondDesktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(secondEvents.Close)
	var secondTurn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, secondDesktop.Call(context.Background(), "turn/start", map[string]any{
		"threadId": threadID,
		"input": []map[string]any{{"type": "text", "text": "Second Desktop",
			"textElements": []any{}}},
	}, &secondTurn))
	waitForRelayTurnCompleted(t, secondEvents.Events(), threadID, secondTurn.Turn.ID)
	secondDesktopBody := <-requestBodies
	require.Contains(t, secondDesktopBody, participant.ID.String())
	require.Contains(t, secondDesktopBody, "Alice")
}

type identityRelayController struct {
	participant participantidentity.Participant
}

func (c identityRelayController) PrepareCall(_ context.Context,
	call codexrelay.Call,
) (codexrelay.CallPlan, error) {
	params := append(json.RawMessage(nil), call.Params...)
	if call.Method == "thread/start" {
		params = participantidentity.AppendDeveloperInstructions(params)
	}
	if call.Method == "turn/start" || call.Method == "turn/steer" {
		params = participantidentity.InjectTurnContext(params, c.participant)
	}
	return codexrelay.CallPlan{Params: params, Forward: true}, nil
}

func (identityRelayController) CompleteCall(_ context.Context, _ codexrelay.Call,
	_ codexrelay.CallPlan, result json.RawMessage, cause error,
) (json.RawMessage, error) {
	return result, cause
}

func (identityRelayController) ResolveInteractive(_ context.Context, _ codex.ServerRequest,
	answer json.RawMessage, _ codexrelay.Role,
) (bool, json.RawMessage, error) {
	return true, answer, nil
}

func TestRealCodexRelayWorkerInitiatesAndOrdinaryDesktopRemainsFunctional(t *testing.T) {
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": "worker-resp"}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "worker-message",
				"content": []map[string]any{{"type": "output_text", "text": "worker done"}},
			}}, completedResponse("worker-resp")))
	}))
	t.Cleanup(responses.Close)

	root := temporaryDir(t, "tyrs-real-relay-multiclient-")
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	config := fmt.Sprintf(`model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "Relay multi-client mock"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`, responses.URL+"/v1")
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600))
	appSocket := filepath.Join(root, "app.sock")
	process := exec.Command(fixedCodexBinary(t), "app-server", "--listen", "unix://"+appSocket)
	process.Dir = workspace
	process.Env = append(os.Environ(), "CODEX_HOME="+home, "HOME="+root, "RUST_LOG=warn")
	require.NoError(t, process.Start())
	t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	waitForUnixSocket(t, appSocket)
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: filepath.Join(root, "relay.sock"), UpstreamSocketPath: appSocket,
		Controller: codexrelay.PassThroughController{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })

	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: relay.SocketPath(), RequestTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = desktop.Close() })
	desktopAll := desktop.Subscribe(codex.ThreadFilter{})
	t.Cleanup(desktopAll.Close)

	var models map[string]any
	require.NoError(t, desktop.Call(context.Background(), "model/list", map[string]any{}, &models))
	threadID, err := worker.StartThread(context.Background(), mustRealJSON(map[string]any{
		"cwd": workspace, "model": "mock-model", "approvalPolicy": "never", "sandbox": "read-only",
	}))
	require.NoError(t, err)
	waitForRealRelayEvent(t, desktopAll.Events(), "thread/started", threadID, "")

	var resumed map[string]any
	require.NoError(t, desktop.Call(context.Background(), "thread/resume",
		map[string]any{"threadId": threadID}, &resumed))
	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, desktop.Call(context.Background(), "thread/list", map[string]any{}, &listed))
	require.NotEmpty(t, listed.Data)

	workerEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(workerEvents.Close)
	runtime := codex.NewRuntime(worker)
	turnID, err := runtime.StartTurn(context.Background(), threadID,
		ports.TurnInput{Text: "Started from the Discord client."})
	require.NoError(t, err)
	waitForRelayTurnCompleted(t, workerEvents.Events(), threadID, turnID)
	waitForRelayTurnCompleted(t, desktopAll.Events(), threadID, turnID)
	require.Equal(t, int64(1), relay.Stats().UpstreamConnections)
}

func mustRealJSON(value any) json.RawMessage {
	result, _ := json.Marshal(value)
	return result
}

func waitForRealRelayEvent(t *testing.T, events <-chan codex.Event, method, threadID,
	turnID string,
) {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Method != method {
				continue
			}
			var params struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
				Thread   struct {
					ID string `json:"id"`
				} `json:"thread"`
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			if params.ThreadID == "" {
				params.ThreadID = params.Thread.ID
			}
			if params.TurnID == "" {
				params.TurnID = params.Turn.ID
			}
			if params.ThreadID == threadID && (turnID == "" || params.TurnID == turnID) {
				return
			}
		case <-timer.C:
			t.Fatalf("等待真实 Relay 事件 %s 超时", method)
		}
	}
}
