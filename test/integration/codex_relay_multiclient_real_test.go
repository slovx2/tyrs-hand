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

	requireModernCodexModelCatalog(t, desktop)
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

func requireModernCodexModelCatalog(t *testing.T, client *codex.SocketClient) {
	t.Helper()
	var catalog struct {
		Data []struct {
			ID                        string `json:"id"`
			IsDefault                 bool   `json:"isDefault"`
			SupportedReasoningEfforts []struct {
				ReasoningEffort string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
			ServiceTiers []struct {
				ID string `json:"id"`
			} `json:"serviceTiers"`
			AdditionalSpeedTiers []string `json:"additionalSpeedTiers"`
		} `json:"data"`
	}
	require.NoError(t, client.Call(context.Background(), "model/list", map[string]any{}, &catalog))
	for _, model := range catalog.Data {
		if model.ID != "gpt-5.6-sol" {
			continue
		}
		efforts := make([]string, 0, len(model.SupportedReasoningEfforts))
		for _, effort := range model.SupportedReasoningEfforts {
			efforts = append(efforts, effort.ReasoningEffort)
		}
		tiers := make([]string, 0, len(model.ServiceTiers))
		for _, tier := range model.ServiceTiers {
			tiers = append(tiers, tier.ID)
		}
		require.True(t, model.IsDefault)
		require.Contains(t, efforts, "max")
		require.Contains(t, efforts, "ultra")
		require.Contains(t, tiers, "priority")
		require.Contains(t, model.AdditionalSpeedTiers, "fast")
		return
	}
	t.Fatal("真实 Codex model/list 没有返回 gpt-5.6-sol")
}

func TestRealCodexRelayDesktopSelectsFastServiceTierWithAPIKeyUpstream(t *testing.T) {
	requestBody := make(chan map[string]any, 1)
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
		requestBody <- body
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{
				"id": "service-tier-response",
			}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "service-tier-message",
				"content": []map[string]any{{"type": "output_text", "text": "done"}},
			}},
			completedResponse("service-tier-response"),
		))
	}))
	t.Cleanup(responses.Close)

	root := temporaryDir(t, "tyrs-real-relay-service-tier-")
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	config := fmt.Sprintf(`model = "gpt-5.6-sol"
model_reasoning_effort = "ultra"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "Relay service tier mock"
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
		Controller: serviceTierRelayController{},
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

	var workerAccount struct {
		Account any `json:"account"`
	}
	require.NoError(t, worker.Call(context.Background(), "account/read",
		map[string]any{"refreshToken": false}, &workerAccount))
	require.Nil(t, workerAccount.Account)
	var desktopAccount struct {
		Account struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	require.NoError(t, desktop.Call(context.Background(), "account/read",
		map[string]any{"refreshToken": false}, &desktopAccount))
	require.Equal(t, "chatgpt", desktopAccount.Account.Type)
	requireModernCodexModelCatalog(t, desktop)

	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
		ServiceTier     string `json:"serviceTier"`
	}
	require.NoError(t, desktop.Call(context.Background(), "thread/start", map[string]any{
		"cwd": workspace, "model": "gpt-5.6-sol", "serviceTier": "priority",
		"approvalPolicy": "never", "sandbox": "read-only",
	}, &started))
	require.Equal(t, "gpt-5.6-sol", started.Model)
	require.Equal(t, "ultra", started.ReasoningEffort)
	require.Equal(t, "priority", started.ServiceTier)
	require.NoError(t, desktop.Call(context.Background(), "thread/settings/update", map[string]any{
		"threadId": started.Thread.ID, "model": "gpt-5.6-sol", "effort": "max",
		"serviceTier": "priority",
	}, nil))
	events := desktop.Subscribe(codex.ThreadFilter{ThreadID: started.Thread.ID})
	t.Cleanup(events.Close)
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, desktop.Call(context.Background(), "turn/start", map[string]any{
		"threadId": started.Thread.ID,
		"input": []map[string]any{{"type": "text", "text": "Use Fast.",
			"textElements": []any{}}},
	}, &turn))
	waitForRelayTurnCompleted(t, events.Events(), started.Thread.ID, turn.Turn.ID)
	select {
	case body := <-requestBody:
		require.Equal(t, "priority", body["service_tier"])
		reasoning, ok := body["reasoning"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "max", reasoning["effort"])
	case <-time.After(10 * time.Second):
		t.Fatal("Mock LLM 没有收到 Fast service tier 请求")
	}
	var resumed struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
		ServiceTier     string `json:"serviceTier"`
	}
	require.NoError(t, desktop.Call(context.Background(), "thread/resume",
		map[string]any{"threadId": started.Thread.ID}, &resumed))
	require.Equal(t, "gpt-5.6-sol", resumed.Model)
	require.Equal(t, "max", resumed.ReasoningEffort)
	require.Equal(t, "priority", resumed.ServiceTier)
}

type serviceTierRelayController struct{}

func (serviceTierRelayController) PrepareCall(_ context.Context,
	call codexrelay.Call,
) (codexrelay.CallPlan, error) {
	return codexrelay.CallPlan{Params: call.Params, Forward: true}, nil
}

func (serviceTierRelayController) CompleteCall(_ context.Context, call codexrelay.Call,
	_ codexrelay.CallPlan, result json.RawMessage, cause error,
) (json.RawMessage, error) {
	if call.Method == "account/read" {
		return json.RawMessage(`{"account":{"type":"chatgpt","email":null,` +
			`"planType":"unknown"},"requiresOpenaiAuth":false}`), nil
	}
	return result, cause
}

func (serviceTierRelayController) ResolveInteractive(_ context.Context, _ codex.ServerRequest,
	answer json.RawMessage, _ codexrelay.Role,
) (bool, json.RawMessage, error) {
	return true, answer, nil
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
	var responseNumber atomic.Int32
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		number := responseNumber.Add(1)
		if number == 2 {
			_, _ = fmt.Fprint(response, sse(
				map[string]any{"type": "response.created", "response": map[string]any{"id": "shell-resp"}},
				map[string]any{"type": "response.output_item.done", "item": map[string]any{
					"type": "local_shell_call", "call_id": "shell-call", "status": "completed",
					"action": map[string]any{"type": "exec",
						"command": []string{"/bin/sh", "-lc", "printf relay-shell"}},
				}}, completedResponse("shell-resp")))
			return
		}
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{
				"id": fmt.Sprintf("worker-resp-%d", number),
			}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": fmt.Sprintf("worker-message-%d", number),
				"content": []map[string]any{{"type": "output_text", "text": "worker done"}},
			}}, completedResponse(fmt.Sprintf("worker-resp-%d", number))))
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
	approvalRequests := make(chan codex.ServerRequest, 1)
	desktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: relay.SocketPath(), RequestTimeout: 30 * time.Second,
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			approvalRequests <- request
			return map[string]string{"decision": "accept"}, nil
		},
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
	workerEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(workerEvents.Close)
	runtime := codex.NewRuntime(worker)
	firstTurnID, err := runtime.StartTurn(context.Background(), threadID,
		ports.TurnInput{Text: "Persist the worker-created thread."})
	require.NoError(t, err)
	waitForRelayTurnCompleted(t, workerEvents.Events(), threadID, firstTurnID)
	waitForRelayTurnCompleted(t, desktopAll.Events(), threadID, firstTurnID)

	var resumed map[string]any
	require.NoError(t, desktop.Call(context.Background(), "thread/resume",
		map[string]any{"threadId": threadID}, &resumed))
	secondDesktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: relay.SocketPath(), RequestTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = secondDesktop.Close() })
	require.NoError(t, secondDesktop.Call(context.Background(), "thread/resume",
		map[string]any{"threadId": threadID}, &resumed))
	secondEvents := secondDesktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(secondEvents.Close)

	require.NoError(t, desktop.Call(context.Background(), "thread/name/set", map[string]any{
		"threadId": threadID, "name": "Worker 与 Desktop 最新标题",
	}, nil))
	waitForRealRelayEvent(t, desktopAll.Events(), "thread/name/updated", threadID, "")
	waitForRealRelayEvent(t, secondEvents.Events(), "thread/name/updated", threadID, "")
	require.NoError(t, desktop.Call(context.Background(), "thread/settings/update", map[string]any{
		"threadId": threadID, "approvalPolicy": "never",
		"permissions": ":danger-full-access",
	}, nil))
	waitForRealRelayEvent(t, desktopAll.Events(), "thread/settings/updated", threadID, "")
	waitForRealRelayEvent(t, secondEvents.Events(), "thread/settings/updated", threadID, "")

	require.NoError(t, secondDesktop.Close())
	require.NoError(t, desktop.Close())

	thirdDesktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: relay.SocketPath(), RequestTimeout: 30 * time.Second,
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			approvalRequests <- request
			return map[string]string{"decision": "accept"}, nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = thirdDesktop.Close() })
	thirdEvents := thirdDesktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(thirdEvents.Close)
	var current struct {
		Thread struct {
			Name string `json:"name"`
		} `json:"thread"`
		ApprovalPolicy string         `json:"approvalPolicy"`
		Sandbox        map[string]any `json:"sandbox"`
	}
	require.NoError(t, thirdDesktop.Call(context.Background(), "thread/resume",
		map[string]any{"threadId": threadID}, &current))
	require.Equal(t, "Worker 与 Desktop 最新标题", current.Thread.Name)
	require.Equal(t, "never", current.ApprovalPolicy)
	require.Equal(t, "dangerFullAccess", current.Sandbox["type"])
	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, thirdDesktop.Call(context.Background(), "thread/list", map[string]any{}, &listed))
	require.NotEmpty(t, listed.Data)

	turnID, err := runtime.StartTurn(context.Background(), threadID,
		ports.TurnInput{Text: "Started from the Discord client."})
	require.NoError(t, err)
	waitForRelayTurnCompleted(t, workerEvents.Events(), threadID, turnID)
	waitForRelayTurnCompleted(t, thirdEvents.Events(), threadID, turnID)
	select {
	case request := <-approvalRequests:
		t.Fatalf("完全访问的下一 Turn 不应请求审批: %s", request.Method)
	case <-time.After(200 * time.Millisecond):
	}
	require.Equal(t, int64(1), relay.Stats().UpstreamConnections)
}

func TestRealCodexRelayEphemeralThreadStaysDesktopOnly(t *testing.T) {
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": "ephemeral-resp"}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "ephemeral-message",
				"content": []map[string]any{{"type": "output_text", "text": "helper title"}},
			}}, completedResponse("ephemeral-resp")))
	}))
	t.Cleanup(responses.Close)
	root := temporaryDir(t, "tyrs-real-relay-ephemeral-")
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	config := fmt.Sprintf(`model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "Relay ephemeral mock"
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

	controller := &realRecordingController{}
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: filepath.Join(root, "relay.sock"), UpstreamSocketPath: appSocket,
		Controller: controller,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relay.Close() })
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	workerEvents := worker.Subscribe(codex.ThreadFilter{})
	t.Cleanup(workerEvents.Close)
	desktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: relay.SocketPath(), RequestTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = desktop.Close() })

	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	require.NoError(t, desktop.Call(context.Background(), "thread/start", map[string]any{
		"cwd": workspace, "model": "mock-model", "ephemeral": true,
		"approvalPolicy": "never", "sandbox": "read-only",
	}, &started))
	desktopEvents := desktop.Subscribe(codex.ThreadFilter{ThreadID: started.Thread.ID})
	t.Cleanup(desktopEvents.Close)
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, desktop.Call(context.Background(), "turn/start", map[string]any{
		"threadId": started.Thread.ID,
		"input": []map[string]any{{"type": "text", "text": "Generate a title.",
			"textElements": []any{}}},
	}, &turn))
	waitForRelayTurnCompleted(t, desktopEvents.Events(), started.Thread.ID, turn.Turn.ID)
	nameErr := desktop.Call(context.Background(), "thread/name/set", map[string]any{
		"threadId": started.Thread.ID, "name": "helper title",
	}, nil)
	require.ErrorContains(t, nameErr, "ephemeral thread does not support metadata updates")
	require.Empty(t, controller.Methods())
	deadline := time.NewTimer(200 * time.Millisecond)
	defer deadline.Stop()
	for {
		select {
		case event := <-workerEvents.Events():
			var scope struct {
				ThreadID string `json:"threadId"`
				Thread   struct {
					ID string `json:"id"`
				} `json:"thread"`
			}
			_ = json.Unmarshal(event.Params, &scope)
			if scope.ThreadID == started.Thread.ID || scope.Thread.ID == started.Thread.ID {
				t.Fatalf("Worker 不应收到 ephemeral Thread 事件: %s", event.Method)
			}
		case <-deadline.C:
			return
		}
	}
}

type realRecordingController struct {
	mu      sync.Mutex
	methods []string
}

func (c *realRecordingController) PrepareCall(_ context.Context,
	call codexrelay.Call,
) (codexrelay.CallPlan, error) {
	c.mu.Lock()
	c.methods = append(c.methods, call.Method)
	c.mu.Unlock()
	return codexrelay.CallPlan{Params: call.Params, Forward: true}, nil
}

func (*realRecordingController) CompleteCall(_ context.Context, _ codexrelay.Call,
	_ codexrelay.CallPlan, result json.RawMessage, cause error,
) (json.RawMessage, error) {
	return result, cause
}

func (*realRecordingController) ResolveInteractive(_ context.Context, _ codex.ServerRequest,
	answer json.RawMessage, _ codexrelay.Role,
) (bool, json.RawMessage, error) {
	return true, answer, nil
}

func (c *realRecordingController) Methods() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.methods...)
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
