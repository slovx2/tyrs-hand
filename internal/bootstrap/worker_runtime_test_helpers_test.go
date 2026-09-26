//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func connectBootstrapSSH(t *testing.T, ctx context.Context, entry *hostworker.RuntimeEntry, signer ssh.Signer) (*codex.SocketClient, <-chan struct{}) {
	t.Helper()
	return connectBootstrapSSHWithOptions(t, ctx, entry, signer, codex.SocketClientOptions{})
}

func connectBootstrapSSHWithOptions(t *testing.T, ctx context.Context, entry *hostworker.RuntimeEntry, signer ssh.Signer, options codex.SocketClientOptions) (*codex.SocketClient, <-chan struct{}) {
	t.Helper()
	connection, err := ssh.Dial("tcp", entry.SSH.Addr().String(), &ssh.ClientConfig{
		User: "test", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: 5 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if ssh.FingerprintSHA256(key) != entry.SSH.HostKeyFingerprint() {
				return fmt.Errorf("入口 Host Key 不匹配")
			}
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	closed := make(chan struct{})
	go func() { _ = connection.Wait(); close(closed) }()
	channel, requests, err := connection.OpenChannel("session", nil)
	require.NoError(t, err)
	go ssh.DiscardRequests(requests)
	ok, err := channel.SendRequest("exec", true, ssh.Marshal(struct{ Command string }{"codex app-server proxy"}))
	require.NoError(t, err)
	require.True(t, ok)
	dialer := websocket.Dialer{NetDialContext: func(context.Context, string, string) (net.Conn, error) { return bootstrapSSHConnection{channel}, nil }}
	ws, response, err := dialer.DialContext(ctx, "ws://worker/", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	trace := &bootstrapTrace{MessageTransport: ws}
	t.Cleanup(func() {
		trace.mu.Lock()
		defer trace.mu.Unlock()
		saveBootstrapArtifact(t, "wire", entry.Runtime.Info().Engine, map[string]any{"messages": trace.messages, "protocolErrors": []string{}})
	})
	options.RequestTimeout = 10 * time.Second
	client, err := codex.ConnectTransport(ctx, trace, options)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client, closed
}

type bootstrapTrace struct {
	codex.MessageTransport
	mu       sync.Mutex
	messages []map[string]any
}

func (p *bootstrapTrace) record(direction string, data []byte) {
	var message map[string]any
	if json.Unmarshal(data, &message) != nil {
		return
	}
	message["direction"] = direction
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, message)
}

func (p *bootstrapTrace) ReadMessage() (int, []byte, error) {
	kind, data, err := p.MessageTransport.ReadMessage()
	if err == nil {
		p.record("server", data)
	}
	return kind, data, err
}

func (p *bootstrapTrace) WriteMessage(kind int, data []byte) error {
	p.record("client", data)
	return p.MessageTransport.WriteMessage(kind, data)
}

func saveBootstrapArtifact(t *testing.T, kind string, engine runtimeidentity.Engine, payload any) {
	t.Helper()
	directory := os.Getenv("PROTOCOL_ARTIFACT_DIR")
	if directory == "" {
		return
	}
	cases := []string{"ENTRY-002", "TOOLS-002"}
	if t.Name() == "TestWorkerControlRealSSHBothEngines" {
		cases = []string{"CHANNELS-002"}
		if engine == runtimeidentity.Claude {
			cases = append(cases, "AUTOMATION-001", "AUTOMATION-002", "APPROVAL-006")
		}
	}
	if t.Name() == "TestWorkerControlPermissionsRealSSH" {
		cases = []string{"PERMISSION-009"}
	}
	if t.Name() == "TestWorkerControlMcpRealSSH" {
		cases = []string{"MCP-014"}
	}
	data, err := json.MarshalIndent(map[string]any{"formatVersion": 1,
		"runId": os.Getenv("PROTOCOL_RUN_ID"), "engine": engine, "caseName": t.Name(),
		"caseIds": cases, "kind": kind, "payload": payload}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, kind+"-bootstrap-"+string(engine)+"-"+uuid.NewString()+".json"), data, 0o600))
}

type bootstrapSSHConnection struct{ ssh.Channel }

func (bootstrapSSHConnection) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (bootstrapSSHConnection) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (bootstrapSSHConnection) SetDeadline(time.Time) error      { return nil }
func (bootstrapSSHConnection) SetReadDeadline(time.Time) error  { return nil }
func (bootstrapSSHConnection) SetWriteDeadline(time.Time) error { return nil }

func awaitBootstrapTurn(t *testing.T, ctx context.Context, events *codex.EventSubscription) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("等待真实 Turn 终态超时")
		case event, ok := <-events.Events():
			if !ok {
				t.Fatal("事件流在终态前关闭")
			}
			if event.Method != "turn/completed" {
				continue
			}
			var result struct {
				Turn struct {
					Status string
					Error  any
				} `json:"turn"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &result))
			require.Equal(t, "completed", result.Turn.Status, "%v", result.Turn.Error)
			return
		}
	}
}

func bootstrapEvent(w http.ResponseWriter, kind string, payload map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	payload["type"] = kind
	body, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, body)
}

func bootstrapClaudeStart(w http.ResponseWriter) {
	bootstrapEvent(w, "message_start", map[string]any{"message": map[string]any{
		"id": "msg_boot", "type": "message", "role": "assistant", "model": "mock-claude",
		"content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
}

func bootstrapClaudeEnd(w http.ResponseWriter, reason string) {
	bootstrapEvent(w, "content_block_stop", map[string]any{"index": 0})
	bootstrapEvent(w, "message_delta", map[string]any{"delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 5}})
	bootstrapEvent(w, "message_stop", map[string]any{})
}

func bootstrapModelGitCommit(w http.ResponseWriter, tool string) {
	bootstrapClaudeStart(w)
	bootstrapEvent(w, "content_block_start", map[string]any{"index": 0, "content_block": map[string]any{
		"type": "tool_use", "id": "toolu_bootstrap", "name": tool, "input": map[string]any{}}})
	bootstrapEvent(w, "content_block_delta", map[string]any{"index": 0, "delta": map[string]any{
		"type": "input_json_delta", "partial_json": `{"message":"bootstrap protocol commit"}`}})
	bootstrapClaudeEnd(w, "tool_use")
}

func bootstrapModelText(w http.ResponseWriter, claude bool) {
	if claude {
		bootstrapClaudeStart(w)
		bootstrapEvent(w, "content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		bootstrapEvent(w, "content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "BOOTSTRAP_OK"}})
		bootstrapClaudeEnd(w, "end_turn")
		return
	}
	bootstrapEvent(w, "response.created", map[string]any{"response": map[string]any{"id": "resp_boot"}})
	bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
		"id": "msg_boot", "type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "BOOTSTRAP_OK"}}}})
	bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": "resp_boot",
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15, "input_tokens_details": nil, "output_tokens_details": nil}}})
}
