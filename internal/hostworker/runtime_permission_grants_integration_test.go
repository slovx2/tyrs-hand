//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

const runtimePermissionTool = "mcp__tyrs_permissions__request_permissions"

func TestRuntimePermissionGrantsRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "permission-grants")
}

type runtimePermissionStep struct {
	name   string
	input  map[string]any
	denied bool
}

type runtimePermissionGrantsFixture struct {
	mu              sync.Mutex
	steps           []runtimePermissionStep
	index, sequence int
}

func (f *runtimePermissionGrantsFixture) model(t *testing.T, w http.ResponseWriter, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var request struct {
		Tools    []struct{ Name string }
		Messages []struct{ Content json.RawMessage }
	}
	require.NoError(t, json.Unmarshal(body, &request))
	require.LessOrEqual(t, f.index, len(f.steps), "不得发生脚本外模型调用")
	if f.index == 0 {
		found := false
		for _, tool := range request.Tools {
			found = found || tool.Name == runtimePermissionTool
		}
		require.True(t, found, "真实 SDK 必须向模型公开自有权限工具")
	} else {
		found := false
		for _, message := range request.Messages {
			var blocks []struct {
				Type      string
				ToolUseID string `json:"tool_use_id"`
				IsError   bool   `json:"is_error"`
			}
			if json.Unmarshal(message.Content, &blocks) != nil {
				continue
			}
			for _, block := range blocks {
				if block.Type == "tool_result" && block.ToolUseID == fmt.Sprintf("grant_%d_%d", f.sequence, f.index-1) {
					found = true
					require.Equal(t, f.steps[f.index-1].denied, block.IsError, "真实工具结果必须回到模型且状态匹配")
				}
			}
		}
		require.True(t, found, "不能用预制历史替代真实 SDK 工具结果")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
	}
	event("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("msg_grant_%d_%d", f.sequence, f.index), "type": "message", "role": "assistant", "model": "claude-config-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	stop := "end_turn"
	if f.index < len(f.steps) {
		step := f.steps[f.index]
		event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("grant_%d_%d", f.sequence, f.index), "name": step.name, "input": map[string]any{}}})
		input, err := json.Marshal(step.input)
		require.NoError(t, err)
		event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
		stop = "tool_use"
	} else {
		event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "PERMISSIONS_SSH_DONE"}})
	}
	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 10}})
	event("message_stop", map[string]any{})
	f.index++
}

// PERMISSION-011：真实 SSH/Hub/SDK 权限审批及实际写入；作用域不得跨会话或运行代。
func verifyRuntimePermissionGrants(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connection *ssh.Client, f *runtimePermissionGrantsFixture, root string) {
	t.Helper()
	cwd, a, b := filepath.Join(root, "grant-project"), filepath.Join(root, "grant-a"), filepath.Join(root, "grant-b")
	for _, path := range []string{cwd, a, b} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}
	require.NoError(t, os.Symlink(b, filepath.Join(a, "escape")))
	prompts := make(chan runtimeApprovalPrompt, 8)
	connect := func() (*codex.SocketClient, *protocolTraceTransport) {
		return connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{
			ServerRequestHandler: func(callbackCtx context.Context, request codex.ServerRequest) (any, error) {
				if request.Method != "item/permissions/requestApproval" {
					return map[string]string{"decision": "accept"}, nil
				}
				prompt := runtimeApprovalPrompt{request: request, reply: make(chan any, 1)}
				prompts <- prompt
				select {
				case reply := <-prompt.reply:
					return reply, nil
				case <-callbackCtx.Done():
					return nil, callbackCtx.Err()
				}
			},
		})
	}
	client, trace := connect()
	newThread := func() string {
		return readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": cwd, "sandbox": "read-only", "approvalPolicy": "on-request"}).ID
	}
	threadID := newThread()
	files := func(roots ...string) map[string]any {
		return map[string]any{"fileSystem": map[string]any{"read": nil, "write": roots}}
	}
	grant := func() runtimePermissionStep {
		return runtimePermissionStep{name: runtimePermissionTool, input: map[string]any{"permissions": files(a, b), "reason": "真实 SSH 明确授权目录"}}
	}
	write := func(path string, denied bool) runtimePermissionStep {
		return runtimePermissionStep{name: "Write", input: map[string]any{"file_path": path, "content": "SSH_GRANTED"}, denied: denied}
	}
	start := func(id string, steps ...runtimePermissionStep) string {
		f.mu.Lock()
		f.steps, f.index, f.sequence = steps, 0, f.sequence+1
		f.mu.Unlock()
		var result struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": id, "input": []map[string]string{{"type": "text", "text": "验证 SSH 权限作用域"}}}, &result))
		return result.Turn.ID
	}
	answer := func(scope string) {
		select {
		case prompt := <-prompts:
			var params struct {
				ThreadID, TurnID, ItemID, Cwd string
				Permissions                   json.RawMessage
			}
			require.NoError(t, json.Unmarshal(prompt.request.Params, &params))
			require.Equal(t, threadID, params.ThreadID)
			require.Equal(t, cwd, params.Cwd)
			require.NotEmpty(t, params.ItemID)
			require.NotEmpty(t, params.TurnID)
			expected, err := json.Marshal(files(a, b))
			require.NoError(t, err)
			var proposed map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(params.Permissions, &proposed))
			delete(proposed, "network")
			actual, err := json.Marshal(proposed)
			require.NoError(t, err)
			require.JSONEq(t, string(expected), string(actual))
			prompt.reply <- map[string]any{"permissions": files(a), "scope": scope}
		case <-ctx.Done():
			t.Fatal("真实 SSH 未收到权限审批")
		}
	}
	allowed := filepath.Join(a, "allowed.txt")
	denied := []string{filepath.Join(b, "blocked.txt"), filepath.Join(cwd, "blocked.txt"), filepath.Join(a, "escape", "blocked.txt")}
	turn := start(threadID, grant(), write(allowed, false), write(denied[0], true), write(denied[1], true), write(denied[2], true))
	_, err := os.Stat(allowed)
	require.True(t, os.IsNotExist(err), "回答前不得提前执行写入")
	answer("turn")
	waitSessionTurn(t, ctx, client, threadID, turn)
	data, err := os.ReadFile(allowed)
	require.NoError(t, err)
	require.Equal(t, "SSH_GRANTED", string(data))
	for _, path := range denied {
		_, err := os.Stat(path)
		require.True(t, os.IsNotExist(err))
	}
	expired := filepath.Join(a, "expired.txt")
	waitSessionTurn(t, ctx, client, threadID, start(threadID, write(expired, true)))
	turn = start(threadID, grant(), write(filepath.Join(a, "session.txt"), false))
	answer("session")
	waitSessionTurn(t, ctx, client, threadID, turn)
	reused := filepath.Join(a, "reused.txt")
	waitSessionTurn(t, ctx, client, threadID, start(threadID, write(reused, false)))
	data, err = os.ReadFile(reused)
	require.NoError(t, err)
	require.Equal(t, "SSH_GRANTED", string(data))
	foreign := newThread()
	foreignFile := filepath.Join(a, "foreign.txt")
	waitSessionTurn(t, ctx, client, foreign, start(foreign, write(foreignFile, true)))
	generation := registry.entries[runtimeidentity.Codex].Runtime.Generation()
	trace.expectClose("runtime-restart")
	require.NoError(t, registry.Restart(runtimeidentity.Claude))
	require.Equal(t, generation, registry.entries[runtimeidentity.Codex].Runtime.Generation())
	client, _ = connect()
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": threadID})
	restarted := filepath.Join(a, "restarted.txt")
	waitSessionTurn(t, ctx, client, threadID, start(threadID, write(restarted, true)))
	for _, path := range []string{expired, foreignFile, restarted} {
		_, err := os.Stat(path)
		require.True(t, os.IsNotExist(err))
	}
	select {
	case <-prompts:
		t.Fatal("未请求的权限不得被再次审批继承")
	default:
	}
}
