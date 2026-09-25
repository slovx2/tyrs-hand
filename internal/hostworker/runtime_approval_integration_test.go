//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type runtimeApprovalFixture struct {
	root  string
	calls atomic.Int64
}

func (f *runtimeApprovalFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	step := f.calls.Add(1)
	var tool string
	var input map[string]any
	switch step {
	case 1:
		tool = "AskUserQuestion"
		input = map[string]any{"questions": []map[string]any{{"question": "Continue?", "header": "Confirm", "multiSelect": false, "options": []map[string]string{{"label": "Yes", "description": "Continue"}, {"label": "No", "description": "Stop"}}}}}
	case 2:
		require.Contains(t, string(body), "Yes", "重新连接后的答案必须进入真实模型上下文")
		tool, input = "Bash", map[string]any{"command": "printf approved >> " + filepath.Join(f.root, "reconnected.txt")}
	case 3, 7:
		runtimeTextModel(w, request, "APPROVAL_DONE", fmt.Sprintf("approval-%d", step))
		return
	case 4:
		tool, input = "Bash", map[string]any{"command": "printf forbidden >> " + filepath.Join(f.root, "interrupted.txt")}
	case 5:
		tool, input = "Write", map[string]any{"file_path": filepath.Join(f.root, "restarted.txt"), "content": "forbidden"}
	case 6:
		tool, input = "Write", map[string]any{"file_path": filepath.Join(f.root, "full-access.txt"), "content": "full"}
	default:
		t.Errorf("审批失效后发生未授权模型续写: %d", step)
		http.Error(w, "unexpected model request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
	}
	event("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("msg_approval_%d", step), "type": "message", "role": "assistant", "model": "claude-config-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_approval_%d", step), "name": tool, "input": map[string]any{}}})
	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(encoded)}})
	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 10}})
	event("message_stop", map[string]any{})
}

type runtimeApprovalPrompt struct {
	request   codex.ServerRequest
	reply     chan any
	cancelled chan struct{}
}

// APPROVAL-005：真实 SSH 待办重连，取消和重启失效，完全访问无需审批。
func verifyRuntimeApprovalLifecycle(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connection *ssh.Client, fixture *runtimeApprovalFixture) {
	t.Helper()
	prompts := make(chan runtimeApprovalPrompt, 8)
	var activeTrace *protocolTraceTransport
	connect := func() *codex.SocketClient {
		client, trace := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{
			ServerRequestHandler: func(callbackCtx context.Context, request codex.ServerRequest) (any, error) {
				prompt := runtimeApprovalPrompt{request: request, reply: make(chan any, 1), cancelled: make(chan struct{})}
				prompts <- prompt
				select {
				case reply := <-prompt.reply:
					return reply, nil
				case <-callbackCtx.Done():
					close(prompt.cancelled)
					return map[string]any{"decision": "accept"}, nil
				}
			},
		})
		activeTrace = trace
		return client
	}
	next := func(method string) runtimeApprovalPrompt {
		select {
		case prompt := <-prompts:
			require.Equal(t, method, prompt.request.Method)
			return prompt
		case <-ctx.Done():
			t.Fatal("未收到审批或提问")
			return runtimeApprovalPrompt{}
		}
	}
	cancelled := func(prompt runtimeApprovalPrompt) {
		select {
		case <-prompt.cancelled:
		case <-time.After(2 * time.Second):
			t.Fatalf("旧审批回调没有取消: modelStep=%d method=%s id=%s params=%s", fixture.calls.Load(), prompt.request.Method, prompt.request.ID, prompt.request.Params)
		}
	}
	client := connect()
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": fixture.root, "approvalPolicy": "on-request", "sandbox": "danger-full-access"})
	start := func(text string, full bool) string {
		params := map[string]any{"threadId": thread.ID, "input": []map[string]string{{"type": "text", "text": text}}}
		if full {
			params["approvalPolicy"] = "never"
			params["sandboxPolicy"] = map[string]any{"type": "dangerFullAccess"}
		}
		var result struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", params, &result))
		return result.Turn.ID
	}
	first := start("APPROVAL_RECONNECT", false)
	question := next("item/tool/requestUserInput")
	activeTrace.expectClose("client-disconnect")
	require.NoError(t, client.Close())
	cancelled(question)
	client = connect()
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	resumed := next("item/tool/requestUserInput")
	require.JSONEq(t, string(question.request.ID), string(resumed.request.ID))
	var params struct{ Questions []struct{ ID string } }
	require.NoError(t, json.Unmarshal(resumed.request.Params, &params))
	resumed.reply <- map[string]any{"answers": map[string]any{params.Questions[0].ID: map[string]any{"answers": []string{"Yes"}}}}
	command := next("item/commandExecution/requestApproval")
	activeTrace.expectClose("client-disconnect")
	require.NoError(t, client.Close())
	cancelled(command)
	client = connect()
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	commandAgain := next("item/commandExecution/requestApproval")
	require.JSONEq(t, string(command.request.ID), string(commandAgain.request.ID))
	commandAgain.reply <- map[string]string{"decision": "accept"}
	waitSessionTurn(t, ctx, client, thread.ID, first)
	data, err := os.ReadFile(filepath.Join(fixture.root, "reconnected.txt"))
	require.NoError(t, err)
	require.Equal(t, "approved", string(data), "重连不能重复执行工具")
	interrupted := start("APPROVAL_INTERRUPT", false)
	old := next("item/commandExecution/requestApproval")
	require.NoError(t, client.Call(ctx, "turn/interrupt", map[string]string{"threadId": thread.ID, "turnId": interrupted}, nil))
	cancelled(old)
	start("APPROVAL_RESTART", false)
	old = next("item/fileChange/requestApproval")
	otherGeneration := registry.entries[runtimeidentity.Codex].Runtime.Generation()
	activeTrace.expectClose("runtime-restart")
	require.NoError(t, registry.Restart(runtimeidentity.Claude))
	cancelled(old)
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Codex].Runtime.Generation())
	client = connect()
	restored := readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	for _, turn := range restored.Turns {
		require.NotEqual(t, "inProgress", turn.Status)
	}
	last := start("APPROVAL_FULL_ACCESS", true)
	waitSessionTurn(t, ctx, client, thread.ID, last)
	select {
	case pending := <-prompts:
		t.Fatalf("旧请求重放或完全访问仍发起审批: %s", pending.request.Method)
	default:
	}
	for _, name := range []string{"interrupted.txt", "restarted.txt"} {
		_, err := os.Stat(filepath.Join(fixture.root, name))
		require.True(t, os.IsNotExist(err), "过期审批不能产生副作用")
	}
	data, err = os.ReadFile(filepath.Join(fixture.root, "full-access.txt"))
	require.NoError(t, err)
	require.Equal(t, "full", string(data))
	require.Equal(t, int64(7), fixture.calls.Load(), "只有明确允许的原生执行可以续写模型")
}
