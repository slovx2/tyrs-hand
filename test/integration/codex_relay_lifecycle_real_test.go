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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexrelay"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestRealCodexRelayArchiveWaitsForTurnAndRestoreReachesEveryClient(t *testing.T) {
	var responseCount atomic.Int32
	firstStarted := make(chan struct{})
	firstCanceled := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseFirst) }) })
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		number := responseCount.Add(1)
		if number == 1 {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-request.Context().Done():
				firstCanceled <- struct{}{}
				return
			}
		}
		id := fmt.Sprintf("lifecycle-response-%d", number)
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": id}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "lifecycle-message",
				"content": []map[string]any{{"type": "output_text", "text": "done"}},
			}}, completedResponse(id)))
	}))
	t.Cleanup(responses.Close)

	root := temporaryDir(t, "tyrs-real-relay-lifecycle-")
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	config := fmt.Sprintf(`model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "Lifecycle mock"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`, responses.URL+"/v1")
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"),
		[]byte(config), 0o600))
	appSocket := filepath.Join(root, "app.sock")
	process := exec.Command(fixedCodexBinary(t), "app-server", "--listen", "unix://"+appSocket)
	process.Dir = workspace
	process.Env = append(os.Environ(), "CODEX_HOME="+home, "HOME="+root, "RUST_LOG=warn")
	require.NoError(t, process.Start())
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	})
	waitForUnixSocket(t, appSocket)

	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: filepath.Join(root, "relay.sock"), UpstreamSocketPath: appSocket,
		Controller: codexrelay.PassThroughController{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relay.Close() })
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	firstDesktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: relay.SocketPath(), RequestTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = firstDesktop.Close() })
	secondDesktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: relay.SocketPath(), RequestTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = secondDesktop.Close() })

	threadID, err := worker.StartThread(context.Background(), mustRealJSON(map[string]any{
		"cwd": workspace, "model": "mock-model", "approvalPolicy": "never",
		"sandbox": "read-only",
	}))
	require.NoError(t, err)
	workerEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	firstEvents := firstDesktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	secondEvents := secondDesktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(workerEvents.Close)
	t.Cleanup(firstEvents.Close)
	t.Cleanup(secondEvents.Close)
	runtime := codex.NewRuntime(firstDesktop)
	turnID, err := runtime.StartTurn(context.Background(), threadID,
		ports.TurnInput{Text: "Hold this response."})
	require.NoError(t, err)
	select {
	case <-firstStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Mock LLM 没有收到真实 Codex Turn")
	}

	archiveDone := make(chan error, 1)
	go func() {
		archiveDone <- secondDesktop.Call(context.Background(), "thread/archive",
			map[string]any{"threadId": threadID}, nil)
	}()
	assertNoLifecycleEvent(t, workerEvents.Events(), 300*time.Millisecond,
		"turn/completed", "thread/archived")
	select {
	case <-firstCanceled:
		t.Fatal("归档等待期间真实 app-server 提前取消了活动 LLM 请求")
	default:
	}
	select {
	case err := <-archiveDone:
		t.Fatalf("活动 Turn 完成前 archive RPC 不应返回: %v", err)
	default:
	}

	releaseOnce.Do(func() { close(releaseFirst) })
	waitForRelayTurnCompleted(t, workerEvents.Events(), threadID, turnID)
	waitForRelayTurnCompleted(t, firstEvents.Events(), threadID, turnID)
	waitForRelayTurnCompleted(t, secondEvents.Events(), threadID, turnID)
	select {
	case err := <-archiveDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Turn 完成后 archive RPC 没有返回")
	}
	waitForLifecycleEvent(t, workerEvents.Events(), "thread/archived", threadID)
	waitForLifecycleEvent(t, firstEvents.Events(), "thread/archived", threadID)

	require.NoError(t, worker.Call(context.Background(), "thread/unarchive",
		map[string]any{"threadId": threadID}, nil))
	waitForLifecycleEvent(t, firstEvents.Events(), "thread/unarchived", threadID)
	waitForLifecycleEvent(t, secondEvents.Events(), "thread/unarchived", threadID)
	require.NoError(t, worker.Call(context.Background(), "thread/resume",
		map[string]any{"threadId": threadID}, nil))
	resumedTurnID, err := codex.NewRuntime(worker).StartTurn(context.Background(), threadID,
		ports.TurnInput{Text: "Continue after restore."})
	require.NoError(t, err)
	waitForRelayTurnCompleted(t, workerEvents.Events(), threadID, resumedTurnID)
	waitForRelayTurnCompleted(t, firstEvents.Events(), threadID, resumedTurnID)
	require.Equal(t, int64(1), relay.Stats().UpstreamConnections)
}

func assertNoLifecycleEvent(t *testing.T, events <-chan codex.Event, wait time.Duration,
	methods ...string,
) {
	t.Helper()
	blocked := make(map[string]bool, len(methods))
	for _, method := range methods {
		blocked[method] = true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if blocked[event.Method] {
				t.Fatalf("等待期间不应收到 %s", event.Method)
			}
		case <-timer.C:
			return
		}
	}
}

func waitForLifecycleEvent(t *testing.T, events <-chan codex.Event, method, threadID string) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Method != method {
				continue
			}
			var params struct {
				ThreadID string `json:"threadId"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			require.Equal(t, threadID, params.ThreadID)
			return
		case <-timer.C:
			t.Fatalf("等待真实 Codex %s 超时", method)
		}
	}
}
