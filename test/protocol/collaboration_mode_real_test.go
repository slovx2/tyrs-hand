//go:build integration

package protocol

import (
	"context"
	"fmt"
	"io"
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
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestMobileDefaultModeOverridesPlanSelectedByAnotherClient(t *testing.T) {
	requestBodies := make(chan string, 8)
	var responseNumber atomic.Int32
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		requestBodies <- string(body)
		number := responseNumber.Add(1)
		id := fmt.Sprintf("mode-response-%d", number)
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": id}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "mode-message-" + id,
				"content": []map[string]any{{"type": "output_text", "text": "done"}},
			}}, completedResponse(id)))
	}))
	t.Cleanup(responses.Close)

	root := temporaryDir(t, "tyrs-protocol-mode-")
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	config := fmt.Sprintf(`model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "Protocol mode mock"
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

	hub, err := appserverhub.Start(context.Background(), appserverhub.Options{
		SocketPath: filepath.Join(root, "hub.sock"), UpstreamSocketPath: appSocket,
		Controller: appserverhub.PassThroughController{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, hub.Close()) })
	worker, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: hub.SocketPath(), RequestTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = desktop.Close() })
	secondDesktop := connectRealDesktop(t, hub)

	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	require.NoError(t, desktop.Call(context.Background(), "thread/start", map[string]any{
		"cwd": workspace, "model": "mock-model", "approvalPolicy": "never", "sandbox": "read-only",
	}, &started))
	threadID := started.Thread.ID
	desktopModes := desktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	secondDesktopModes := secondDesktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	workerTurns := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(desktopModes.Close)
	t.Cleanup(secondDesktopModes.Close)
	t.Cleanup(workerTurns.Close)

	desktopPlanTurn := runDesktopPlanTurn(t, desktop, threadID, "desktop-plan-1", "Create a plan.")
	waitForHubCollaborationMode(t, desktopModes.Events(), threadID, "plan")
	waitForHubCollaborationMode(t, secondDesktopModes.Events(), threadID, "plan")
	waitForHubTurnCompleted(t, workerTurns.Events(), threadID, desktopPlanTurn)

	plan := "# 实施计划\n\n1. 修改实现\n2. 运行测试"
	mobilePlanTurn := runMobileDefaultTurn(t, worker, threadID, "mobile-plan-execution",
		codexcontrol.PlanExecutionInstruction(plan))
	waitForHubCollaborationMode(t, desktopModes.Events(), threadID, "default")
	waitForHubCollaborationMode(t, secondDesktopModes.Events(), threadID, "default")
	waitForHubTurnCompleted(t, workerTurns.Events(), threadID, mobilePlanTurn)

	desktopPlanTurn = runDesktopPlanTurn(t, desktop, threadID, "desktop-plan-2", "Plan again.")
	waitForHubCollaborationMode(t, desktopModes.Events(), threadID, "plan")
	waitForHubCollaborationMode(t, secondDesktopModes.Events(), threadID, "plan")
	waitForHubTurnCompleted(t, workerTurns.Events(), threadID, desktopPlanTurn)
	mobilePlanTurn = runMobileDefaultTurn(t, worker, threadID, "mobile-manual-default", "执行")
	waitForHubCollaborationMode(t, desktopModes.Events(), threadID, "default")
	waitForHubCollaborationMode(t, secondDesktopModes.Events(), threadID, "default")
	waitForHubTurnCompleted(t, workerTurns.Events(), threadID, mobilePlanTurn)

	secondDesktopTurn := startRealTurn(t, secondDesktop, threadID,
		"另一台电脑在手机切回直接执行后继续发送")
	waitForHubTurnCompleted(t, workerTurns.Events(), threadID, secondDesktopTurn)
	assertNoCollaborationMode(t, desktopModes.Events(), threadID, "plan", 200*time.Millisecond)
	assertNoCollaborationMode(t, secondDesktopModes.Events(), threadID, "plan", 200*time.Millisecond)

	bodies := make([]string, 0, responseNumber.Load())
	for range responseNumber.Load() {
		bodies = append(bodies, <-requestBodies)
	}
	transcript := strings.Join(bodies, "\n")
	require.Contains(t, transcript, "PLEASE IMPLEMENT THIS PLAN:")
	require.Contains(t, transcript, "执行")
}

func runDesktopPlanTurn(t *testing.T, client *codex.SocketClient, threadID, clientID,
	text string,
) string {
	t.Helper()
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, client.Call(context.Background(), "turn/start", map[string]any{
		"threadId": threadID, "clientUserMessageId": clientID,
		"input": []map[string]any{{"type": "text", "text": text, "textElements": []any{}}},
		"collaborationMode": map[string]any{"mode": "plan", "settings": map[string]any{
			"model": "mock-model", "reasoning_effort": "medium"}},
	}, &result))
	require.NotEmpty(t, result.Turn.ID)
	return result.Turn.ID
}

func runMobileDefaultTurn(t *testing.T, client *appserverhub.Client, threadID, clientID,
	text string,
) string {
	t.Helper()
	turnID, err := codex.NewRuntime(client).StartTurn(context.Background(), threadID, ports.TurnInput{
		Text: text, ClientUserMessageID: clientID,
		CollaborationMode: &ports.CollaborationMode{Mode: "default"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, turnID)
	return turnID
}
