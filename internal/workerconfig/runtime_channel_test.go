package workerconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestRuntimeConfigOverRealControlChannel(t *testing.T) {
	connections := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-credential" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err == nil {
			connections <- connection
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	codexHome, claudeHome := t.TempDir(), t.TempDir()
	finished := make(chan error, 1)
	go func() {
		finished <- RunChannel(ctx, ChannelOptions{ControlURL: server.URL,
			Credential: "test-credential", ProtocolVersion: workerprotocol.Version,
			Service: NewService(codexHome, "codex"), Claude: NewClaudeService(claudeHome)})
	}()
	var connection *websocket.Conn
	select {
	case connection = <-connections:
	case <-time.After(5 * time.Second):
		t.Fatal("控制通道未连接")
	}
	defer func() { _ = connection.Close() }()
	require.NoError(t, connection.SetReadDeadline(time.Now().Add(5*time.Second)))
	var hello workerprotocol.WorkerRPCRequest
	require.NoError(t, connection.ReadJSON(&hello))
	require.Equal(t, "hello", hello.Method)
	require.NoError(t, connection.WriteJSON(workerprotocol.WorkerRPCResponse{ID: hello.ID,
		Type: workerprotocol.MessageTypeResponse, Result: workerprotocol.WorkerHelloResponse{}}))
	call := func(method string, params any) (json.RawMessage, string) {
		t.Helper()
		require.NoError(t, connection.WriteJSON(workerprotocol.WorkerRPCRequest{ID: method,
			Method: method, Params: mustJSON(params)}))
		var response struct {
			ID     string          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		require.NoError(t, connection.ReadJSON(&response))
		require.Equal(t, method, response.ID)
		return response.Result, response.Error
	}
	params := RuntimeRequest{Engine: runtimeidentity.Claude}
	result, failure := call("config.read", params)
	require.Empty(t, failure)
	var current workerprotocol.WorkerConfig
	require.NoError(t, json.Unmarshal(result, &current))
	params.Input = mustJSON(ClaudeProviderInput{Revision: current.Revision, BaseURL: "http://localhost:1234",
		AuthMethod: "api-key", APIKey: "virtual-credential", Model: "claude-test"})
	result, failure = call("config.provider.write", params)
	require.Empty(t, failure)
	require.NotContains(t, string(result), "virtual-credential")
	require.NoError(t, json.Unmarshal(result, &current))
	params.Input = mustJSON(map[string]string{"revision": current.Revision, "content": "channel-instructions"})
	_, failure = call("config.agents.write", params)
	require.Empty(t, failure)
	_, failure = call("config.agents.write", params)
	require.Contains(t, failure, "冲突")
	_, failure = call("config.read", map[string]any{})
	require.Contains(t, failure, "引擎")
	_, failure = call("runtime.restart", RuntimeRequest{Engine: runtimeidentity.Claude})
	require.Contains(t, failure, "未启用")
	content, err := os.ReadFile(filepath.Join(claudeHome, "CLAUDE.md"))
	require.NoError(t, err)
	require.Equal(t, "channel-instructions", string(content))
	require.NoFileExists(t, filepath.Join(codexHome, "settings.json"))
	settings, err := os.ReadFile(filepath.Join(claudeHome, "settings.json"))
	require.NoError(t, err)
	require.True(t, strings.Contains(string(settings), "virtual-credential"))
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("控制通道未关闭")
	}
}
