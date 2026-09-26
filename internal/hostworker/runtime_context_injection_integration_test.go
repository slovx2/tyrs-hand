//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeContextInjectionRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "context-injection")
}

type runtimeContextInjectionFixture struct{ calls atomic.Int64 }

func (f *runtimeContextInjectionFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	t.Helper()
	step := f.calls.Add(1)
	require.LessOrEqual(t, step, int64(2), "上下文注入和恢复不得额外调用模型")
	var modelRequest struct{ Messages json.RawMessage }
	require.NoError(t, json.Unmarshal(body, &modelRequest))
	text := string(modelRequest.Messages)
	require.True(t, strings.Contains(text, "CONTEXT_006_BASE"), "必须保留真实首轮上下文")
	if step == 1 {
		require.False(t, strings.Contains(text, "CONTEXT_006_INJECTED"))
	} else {
		require.Equal(t, 1, strings.Count(text, "CONTEXT_006_INJECTED"), "注入文本必须真实入模且不得重复")
		require.False(t, strings.Contains(text, "CONTEXT_006_REJECTED"), "整批拒绝不能泄漏部分上下文")
	}
	runtimeTextModel(w, request, "CONTEXT_006_DONE", "context-injection-response")
}

// CONTEXT-006：真实 SSH 原生追加零模型、无伪造助手历史，重启后恢复并实际入模。
func verifyRuntimeContextInjection(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connection *ssh.Client, fixture *runtimeContextInjectionFixture, root string) {
	t.Helper()
	connect := func() *codex.SocketClient {
		client, _ := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{})
		return client
	}
	client := connect()
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": root})
	turn := func(text string) {
		events := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
		defer events.Close()
		var started struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{
			"threadId": thread.ID, "input": []map[string]string{{"type": "text", "text": text}},
		}, &started))
		for {
			select {
			case <-ctx.Done():
				t.Fatal("上下文验收没有收到真实 Turn 终态")
			case event, ok := <-events.Events():
				require.True(t, ok)
				if event.Method != "turn/completed" {
					continue
				}
				var params struct{ Turn struct{ ID, Status string } }
				require.NoError(t, json.Unmarshal(event.Params, &params))
				require.Equal(t, started.Turn.ID, params.Turn.ID)
				require.Equal(t, "completed", params.Turn.Status)
				return
			}
		}
	}
	turn("CONTEXT_006_BASE")
	item := func(role, text string) map[string]any {
		return map[string]any{"type": "message", "role": role, "content": []map[string]string{{"type": "input_text", "text": text}}}
	}
	var rpcError *codex.RPCError
	require.ErrorAs(t, client.Call(ctx, "thread/inject_items", map[string]any{
		"threadId": thread.ID, "items": []any{item("user", "CONTEXT_006_REJECTED"), item("assistant", "UNSUPPORTED_ROLE")},
	}, nil), &rpcError)
	require.NoError(t, client.Call(ctx, "thread/inject_items", map[string]any{
		"threadId": thread.ID, "items": []any{item("user", "CONTEXT_006_INJECTED")},
	}, nil))
	require.Equal(t, int64(1), fixture.calls.Load(), "追加必须零模型")
	var history struct {
		Thread struct{ Turns []json.RawMessage }
	}
	require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true}, &history))
	require.Len(t, history.Thread.Turns, 1, "追加不得制造助手 Turn")
	codexGeneration := registry.entries[runtimeidentity.Codex].Runtime.Generation()
	require.NoError(t, registry.Restart(runtimeidentity.Claude))
	require.Equal(t, codexGeneration, registry.entries[runtimeidentity.Codex].Runtime.Generation())
	client = connect()
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	require.Equal(t, int64(1), fixture.calls.Load(), "重启和恢复不能请求模型")
	turn("CONTEXT_006_CONTINUE")
	require.Equal(t, int64(2), fixture.calls.Load())
}
