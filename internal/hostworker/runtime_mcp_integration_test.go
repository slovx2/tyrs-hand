//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type runtimeMcpFixture struct {
	calls atomic.Int64
}

func (f *runtimeMcpFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	step := f.calls.Add(1)
	if step == 2 {
		require.Contains(t, string(body), "MCP_ACTION_accept", "MCP 回答必须经过真实 SDK 进入模型")
		runtimeTextModel(w, request, "MCP_SSH_DONE", "mcp-ssh-done")
		return
	}
	if step != 1 {
		t.Error("MCP 双客户端回答触发了重复模型请求")
		http.Error(w, "unexpected model call", http.StatusBadRequest)
		return
	}
	require.Contains(t, string(body), "mcp__fixture__confirm_fixture")
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
	}
	event("message_start", map[string]any{"message": map[string]any{"id": "msg_mcp_ssh", "type": "message", "role": "assistant", "model": "claude-config-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_mcp_ssh", "name": "mcp__fixture__confirm_fixture", "input": map[string]any{}}})
	event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": "{}"}})
	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 5}})
	event("message_stop", map[string]any{})
}

// MCP-005：两个客户端通过真实 SSH 同时回答，Hub 只让一个答案到达原生工具。
func verifyRuntimeMcpElicitation(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connection *ssh.Client) {
	t.Helper()
	var received atomic.Int64
	ready := make(chan struct{})
	requests := make(chan codex.ServerRequest, 2)
	clients := make([]*codex.SocketClient, 0, 2)
	for _, name := range []string{"desktop", "phone"} {
		client := connectRuntimeSSHWithOptions(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{
			ClientName: name, ServerRequestHandler: func(callbackCtx context.Context, request codex.ServerRequest) (any, error) {
				if request.Method != "mcpServer/elicitation/request" {
					return nil, fmt.Errorf("未预期的 MCP 回调 %s", request.Method)
				}
				requests <- request
				if received.Add(1) == 2 {
					close(ready)
				}
				select {
				case <-ready:
					return map[string]any{"action": "accept", "content": map[string]any{"value": name}}, nil
				case <-callbackCtx.Done():
					return nil, callbackCtx.Err()
				}
			},
		})
		clients = append(clients, client)
	}
	root := registry.entries[runtimeidentity.Claude].Runtime.WorkspaceRoot()
	file := filepath.Join(root, "mcp-effect.txt")
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	fixture := filepath.Join(filepath.Dir(filepath.Dir(os.Getenv("TYRS_HAND_TEST_CLAUDE_BIN"))), "test", "fixtures", "mcp-interactive-server.mjs")
	thread := readSessionThread(t, ctx, clients[0], "thread/start", map[string]any{
		"cwd": root, "approvalPolicy": "never", "sandbox": "danger-full-access",
		"config": map[string]any{"mcp_servers": map[string]any{"fixture": map[string]any{
			"command": node, "args": []string{fixture}, "env": map[string]string{"FIXTURE_EFFECT_PATH": file},
		}}},
	})
	readSessionThread(t, ctx, clients[1], "thread/resume", map[string]any{"threadId": thread.ID})
	subscriptions := []*codex.EventSubscription{clients[0].Subscribe(codex.ThreadFilter{ThreadID: thread.ID}), clients[1].Subscribe(codex.ThreadFilter{ThreadID: thread.ID})}
	for _, sub := range subscriptions {
		t.Cleanup(sub.Close)
	}
	var started struct{ Turn struct{ ID string } }
	require.NoError(t, clients[0].Call(ctx, "turn/start", map[string]any{"threadId": thread.ID, "input": []map[string]any{{"type": "text", "text": "MCP_SSH_ELICITATION"}}}, &started))
	waitSessionTurn(t, ctx, clients[0], thread.ID, started.Turn.ID)
	require.Equal(t, int64(2), received.Load(), "两个 SSH 客户端都必须收到同一请求")
	first, second := <-requests, <-requests
	require.JSONEq(t, string(first.Params), string(second.Params))
	var params struct{ ThreadID, TurnID, ServerName, Mode string }
	require.NoError(t, json.Unmarshal(first.Params, &params))
	require.Equal(t, thread.ID, params.ThreadID)
	require.Equal(t, started.Turn.ID, params.TurnID)
	require.Equal(t, "fixture", params.ServerName)
	require.Equal(t, "form", params.Mode)
	contents, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Contains(t, []string{"desktop\n", "phone\n"}, string(contents))
	require.Equal(t, 1, strings.Count(string(contents), "\n"), "只允许一个回答产生文件副作用")
	for _, sub := range subscriptions {
		resolved := 0
		for {
			select {
			case event := <-sub.Events():
				if event.Method == "serverRequest/resolved" {
					resolved++
				}
				if event.Method == "turn/completed" {
					require.Equal(t, 1, resolved)
					goto next
				}
			case <-ctx.Done():
				t.Fatal("未收到 MCP 回合终态")
			}
		}
	next:
	}
}
