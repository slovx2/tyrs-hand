//go:build integration

package hostworker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type runtimeMcpStream struct {
	id     json.RawMessage
	params json.RawMessage
	send   chan map[string]any
	closed chan struct{}
}

type runtimeMcpStreamFixture struct {
	started chan *runtimeMcpStream
	calls   atomic.Int64
}

func newRuntimeMcpStreamFixture() *runtimeMcpStreamFixture {
	return &runtimeMcpStreamFixture{started: make(chan *runtimeMcpStream, 16)}
}

// 回环 hosted-apps 服务使用官方 events/stream 方言；不会调用公网或读取个人账户。
func (f *runtimeMcpStreamFixture) serve(t *testing.T, w http.ResponseWriter, request *http.Request) {
	t.Helper()
	if request.URL.Path != "/api/codex/ps/mcp" {
		// 原生 apps 发现接口；本专项没有安装应用或模型调用。
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apps":[],"connectors":[],"items":[]}`))
		return
	}
	if request.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	require.NoError(t, json.NewDecoder(http.MaxBytesReader(w, request.Body, 1<<20)).Decode(&message))
	if message.Method == "notifications/initialized" || message.Method == "notifications/cancelled" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if message.Method == "events/stream" {
		stream := &runtimeMcpStream{id: message.ID, params: message.Params,
			send: make(chan map[string]any, 8), closed: make(chan struct{})}
		f.calls.Add(1)
		f.started <- stream
		defer close(stream.closed)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case <-request.Context().Done():
				return
			case event := <-stream.send:
				data, err := json.Marshal(event)
				require.NoError(t, err)
				if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", data); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	}
	var result any
	switch message.Method {
	case "initialize":
		w.Header().Set("mcp-session-id", "local-stream-session")
		result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
			"serverInfo": map[string]string{"name": "native-stream-fixture", "version": "1.0.0"}}
	case "tools/list":
		result = map[string]any{"tools": []any{}}
	case "ping":
		result = map[string]any{}
	default:
		t.Errorf("非预期 MCP 方法 %s", message.Method)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result}))
}

func (s *runtimeMcpStream) event(method, marker string) {
	s.send <- map[string]any{"jsonrpc": "2.0", "method": method, "params": map[string]any{
		"_meta": map[string]any{"io.modelcontextprotocol/subscriptionId": s.id}, "marker": marker,
	}}
}
