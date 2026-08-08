//go:build integration

package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
	"github.com/stretchr/testify/require"
)

func TestOfficialUnixWebSocketSupportsTransparentLateSubscriber(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		_ *http.Request,
	) {
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created",
				"response": map[string]any{"id": "official-response"}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "official-message",
				"content": []map[string]any{{"type": "output_text", "text": "done"}},
			}},
			completedResponse("official-response"),
		))
	}))
	t.Cleanup(upstream.Close)

	socketPath, workspace := startOfficialAppServer(t, upstream.URL)
	first, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: socketPath,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })

	var started struct {
		Thread officialapp.Thread `json:"thread"`
	}
	require.NoError(t, first.Call(context.Background(), "thread/start", map[string]any{
		"cwd": workspace, "model": "mock-model", "runtimeWorkspaceRoots": []string{workspace},
	}, &started))
	require.NotEmpty(t, started.Thread.ID)
	events := first.Subscribe(codex.ThreadFilter{ThreadID: started.Thread.ID})
	t.Cleanup(events.Close)
	var turn struct {
		Turn officialapp.Turn `json:"turn"`
	}
	require.NoError(t, first.Call(context.Background(), "turn/start", map[string]any{
		"threadId": started.Thread.ID, "clientUserMessageId": "transparent-message-1",
		"input": []officialapp.UserInput{officialapp.TextInput("reply done")},
		"model": "mock-model",
	}, &turn))
	waitOfficialTurnCompleted(t, events.Events(), started.Thread.ID, turn.Turn.ID)

	second := connectThroughOpaqueTCPBridge(t, socketPath)
	t.Cleanup(func() { _ = second.Close() })
	var page struct {
		Data []officialapp.Thread `json:"data"`
	}
	require.NoError(t, second.Call(context.Background(), "thread/list", map[string]any{
		"limit": 100, "archived": false, "sortKey": "updated_at", "sortDirection": "desc",
	}, &page))
	require.Contains(t, threadIDs(page.Data), started.Thread.ID)
	thread, err := officialapp.ReadThread(context.Background(), second, started.Thread.ID)
	require.NoError(t, err)
	require.NotNil(t, thread.FindClientMessage("transparent-message-1"))
	require.NotEmpty(t, thread.Turns)
}

func startOfficialAppServer(t *testing.T, upstreamURL string) (string, string) {
	t.Helper()
	root := temporaryDir(t, "tyrs-official-protocol-")
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	config := fmt.Sprintf(`model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "Official protocol mock"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`, upstreamURL+"/v1")
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600))
	socketPath := filepath.Join(root, "app-server.sock")
	process := exec.Command(fixedCodexBinary(t), "app-server", "--listen", "unix://"+socketPath)
	process.Dir = workspace
	process.Env = append(os.Environ(), "CODEX_HOME="+home, "HOME="+root, "RUST_LOG=warn")
	require.NoError(t, process.Start())
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	})
	require.Eventually(t, func() bool {
		info, statErr := os.Stat(socketPath)
		return statErr == nil && info.Mode()&os.ModeSocket != 0
	}, 10*time.Second, 20*time.Millisecond)
	return socketPath, workspace
}

func connectThroughOpaqueTCPBridge(t *testing.T, socketPath string) *codex.SocketClient {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		local, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		upstream, dialErr := net.Dial("unix", socketPath)
		if dialErr != nil {
			_ = local.Close()
			return
		}
		copyDone := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(upstream, local); copyDone <- struct{}{} }()
		go func() { _, _ = io.Copy(local, upstream); copyDone <- struct{}{} }()
		<-copyDone
		_ = local.Close()
		_ = upstream.Close()
		<-copyDone
	}()
	connection, response, err := websocket.DefaultDialer.DialContext(context.Background(),
		"ws://"+listener.Addr().String()+"/", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	client, err := codex.ConnectTransport(context.Background(), connection,
		codex.SocketClientOptions{})
	require.NoError(t, err)
	return client
}

func waitOfficialTurnCompleted(t *testing.T, events <-chan codex.Event, threadID,
	turnID string,
) {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Method != "turn/completed" {
				continue
			}
			var value struct {
				ThreadID string           `json:"threadId"`
				Turn     officialapp.Turn `json:"turn"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &value))
			if value.ThreadID == threadID && value.Turn.ID == turnID {
				return
			}
		case <-timer.C:
			t.Fatal("等待官方 Turn 完成超时")
		}
	}
}

func threadIDs(threads []officialapp.Thread) []string {
	result := make([]string, 0, len(threads))
	for _, thread := range threads {
		result = append(result, thread.ID)
	}
	return result
}
