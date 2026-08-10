package hostworker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func TestServeDesktopUsesManagedProtocolMiddleware(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "tyrs-tunnel-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(directory) }()
	socketPath := filepath.Join(directory, "app-server.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	received := make(chan []byte, 1)
	upstreamServer := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		connection, upgradeErr := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool {
			return true
		}}).Upgrade(response, request, nil)
		require.NoError(t, upgradeErr)
		defer func() { _ = connection.Close() }()
		_, payload, readErr := connection.ReadMessage()
		require.NoError(t, readErr)
		received <- payload
		require.NoError(t, connection.WriteMessage(websocket.TextMessage,
			[]byte(`{"id":1,"result":{}}`)))
	})}
	go func() { _ = upstreamServer.Serve(listener) }()
	defer func() { _ = upstreamServer.Close() }()

	runtime := &Runtime{socketPath: socketPath, options: RuntimeOptions{
		BrowserMCPURL:      "http://browser/mcp",
		BrowserDynamicTool: json.RawMessage(`{"type":"namespace","name":"browser_files"}`),
	}}
	serverSide, clientSide := net.Pipe()
	serveDone := make(chan error, 1)
	go func() { serveDone <- runtime.ServeDesktop(serverSide) }()
	dialer := websocket.Dialer{NetDialContext: func(context.Context, string, string) (net.Conn, error) {
		return clientSide, nil
	}}
	client, response, err := dialer.DialContext(context.Background(), "ws://desktop/", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	require.NoError(t, client.WriteMessage(websocket.TextMessage,
		[]byte(`{"id":1,"method":"thread/start","params":{"cwd":"/workspace"}}`)))
	select {
	case payload := <-received:
		var message struct {
			ID     json.RawMessage `json:"id"`
			Params map[string]any  `json:"params"`
		}
		require.NoError(t, json.Unmarshal(payload, &message))
		require.JSONEq(t, "1", string(message.ID))
		require.Contains(t, message.Params, "config")
		require.Contains(t, message.Params, "dynamicTools")
	case <-time.After(time.Second):
		t.Fatal("Desktop thread/start 没有到达 App Server")
	}
	_, _, err = client.ReadMessage()
	require.NoError(t, err)
	_ = client.Close()
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("Desktop 隧道没有结束")
	}
}

func TestRewriteManagedThreadStartAddsBrowserCapabilities(t *testing.T) {
	tool := json.RawMessage(`{"type":"namespace","name":"browser_files","tools":[]}`)
	payload := []byte(`{"id":7,"method":"thread/start","params":{"cwd":"/workspace/demo",` +
		`"config":{"mcp_servers":{"custom":{"command":"custom"}}},` +
		`"dynamicTools":[{"type":"namespace","name":"existing","tools":[]}]}}`)

	rewritten := rewriteManagedThreadRequest(payload, RuntimeOptions{
		BrowserMCPURL: "http://127.0.0.1:8931/mcp", BrowserDynamicTool: tool,
	})
	var message struct {
		ID     json.RawMessage `json:"id"`
		Params map[string]any  `json:"params"`
	}
	require.NoError(t, json.Unmarshal(rewritten, &message))
	require.JSONEq(t, "7", string(message.ID))
	config := message.Params["config"].(map[string]any)
	servers := config["mcp_servers"].(map[string]any)
	require.Contains(t, servers, "custom")
	chrome := servers["chrome"].(map[string]any)
	require.Equal(t, "http://127.0.0.1:8931/mcp", chrome["url"])
	require.Equal(t, codex.BrowserMCPWorkerTokenEnvironment, chrome["bearer_token_env_var"])
	tools := message.Params["dynamicTools"].([]any)
	require.Len(t, tools, 2)
	require.Equal(t, "browser_files", tools[1].(map[string]any)["name"])
	policy := config["shell_environment_policy"].(map[string]any)
	require.ElementsMatch(t, []any{codex.BrowserMCPWorkerTokenEnvironment,
		codex.BrowserMCPDesktopTokenEnvironment}, policy["exclude"])
}

func TestRewriteManagedThreadRequestPreservesOverridesAndExcludesEphemeral(t *testing.T) {
	options := RuntimeOptions{BrowserMCPURL: "http://browser/mcp",
		BrowserDynamicTool: json.RawMessage(`{"type":"namespace","name":"browser_files"}`)}
	existing := []byte(`{"id":1,"method":"thread/start","params":{"config":{` +
		`"mcp_servers":{"chrome":{"url":"http://custom/mcp"}}},` +
		`"dynamicTools":[{"name":"browser_files"}]}}`)
	var message struct {
		Params map[string]any `json:"params"`
	}
	require.NoError(t, json.Unmarshal(rewriteManagedThreadRequest(existing, options), &message))
	servers := message.Params["config"].(map[string]any)["mcp_servers"].(map[string]any)
	require.Equal(t, "http://custom/mcp", servers["chrome"].(map[string]any)["url"])
	require.Len(t, message.Params["dynamicTools"].([]any), 1)

	ephemeral := []byte(`{"id":2,"method":"thread/start","params":{"ephemeral":true}}`)
	require.Equal(t, ephemeral, rewriteManagedThreadRequest(ephemeral, options))
}

func TestRewriteManagedResumeAddsMCPWithoutDynamicTools(t *testing.T) {
	payload := []byte(`{"id":4,"method":"thread/resume","params":{"threadId":"thread-1"}}`)
	rewritten := rewriteManagedThreadRequest(payload, RuntimeOptions{
		BrowserMCPURL:      "http://browser/mcp",
		BrowserDynamicTool: json.RawMessage(`{"type":"namespace","name":"browser_files"}`),
	})
	var message struct {
		Params map[string]any `json:"params"`
	}
	require.NoError(t, json.Unmarshal(rewritten, &message))
	require.NotNil(t, message.Params["config"])
	require.NotContains(t, message.Params, "dynamicTools")
}

func TestManagedBrowserToolRequestOnlyClaimsBrowserFiles(t *testing.T) {
	namespace := "browser_files"
	payload, err := json.Marshal(map[string]any{
		"id": 11, "method": "item/tool/call", "params": codex.ToolCallRequest{
			ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-1",
			Namespace: &namespace, Tool: "stage_file", Arguments: json.RawMessage(`{"source":"a"}`),
		},
	})
	require.NoError(t, err)
	request, ok := managedBrowserToolRequest(payload)
	require.True(t, ok)
	require.JSONEq(t, "11", string(request.ID))
	require.Equal(t, "stage_file", request.Call.Tool)

	other := "git"
	payload, err = json.Marshal(map[string]any{"id": 12, "method": "item/tool/call",
		"params": codex.ToolCallRequest{Namespace: &other}})
	require.NoError(t, err)
	_, ok = managedBrowserToolRequest(payload)
	require.False(t, ok)
}
