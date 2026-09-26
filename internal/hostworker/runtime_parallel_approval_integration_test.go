//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeParallelApprovalsRealSSHBothEngines(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "parallel-approvals")
}

type runtimeParallelApprovalFixture struct {
	root        string
	codex       runtimeCodexApprovalFixture
	claudeCalls atomic.Int64
}

func (f *runtimeParallelApprovalFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, engine runtimeidentity.Engine, body []byte) {
	if engine == runtimeidentity.Codex {
		require.NotContains(t, string(body), "PARALLEL_CLAUDE_")
		f.codex.model(t, w, body)
		return
	}
	require.NotContains(t, string(body), "CODEX_APPROVAL_")
	step := f.claudeCalls.Add(1)
	require.LessOrEqual(t, step, int64(4))
	decision := "accept"
	if strings.Contains(string(body), "PARALLEL_CLAUDE_decline") {
		decision = "decline"
	}
	id := "parallel-claude-" + decision
	if step%2 == 0 {
		var input struct {
			Messages []struct{ Content json.RawMessage }
		}
		require.NoError(t, json.Unmarshal(body, &input))
		found := false
		for _, message := range input.Messages {
			var blocks []struct {
				Type      string
				ToolUseID string `json:"tool_use_id"`
				IsError   bool   `json:"is_error"`
			}
			if json.Unmarshal(message.Content, &blocks) != nil {
				continue
			}
			for _, block := range blocks {
				if block.Type == "tool_result" && block.ToolUseID == id {
					found = true
					require.Equal(t, decision == "decline", block.IsError)
				}
			}
		}
		require.True(t, found, "所属审批的真实工具结果必须回模")
		runtimeTextModel(w, request, "PARALLEL_CLAUDE_DONE", id)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, data map[string]any) {
		data["type"] = kind
		encoded, err := json.Marshal(data)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
		require.NoError(t, err)
	}
	event("message_start", map[string]any{"message": map[string]any{"id": id, "type": "message", "role": "assistant", "model": "claude-config-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": id, "name": "Write", "input": map[string]any{}}})
	args, err := json.Marshal(map[string]any{"file_path": filepath.Join(f.root, "project", id+".txt"), "content": decision})
	require.NoError(t, err)
	event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 5}})
	event("message_stop", map[string]any{})
}

// ISOLATION-005：同一 Worker 两个真实原生审批同时挂起，分别允许及拒绝。
// 原生会话和请求 ID 自然生成，不将本专项宣称为强制同 ID 碰撞验收。
func verifyRuntimeParallelApprovals(t *testing.T, ctx context.Context, connections map[runtimeidentity.Engine]*ssh.Client, f *runtimeParallelApprovalFixture) {
	engines := []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude}
	clients := map[runtimeidentity.Engine]*codex.SocketClient{}
	prompts := map[runtimeidentity.Engine]chan runtimeApprovalPrompt{}
	for _, engine := range engines {
		prompts[engine] = make(chan runtimeApprovalPrompt, 4)
		clients[engine] = connectRuntimeSSHWithOptions(t, ctx, connections[engine], engine, codex.SocketClientOptions{
			ServerRequestHandler: func(callbackCtx context.Context, request codex.ServerRequest) (any, error) {
				prompt := runtimeApprovalPrompt{request: request, reply: make(chan any, 1)}
				select {
				case prompts[engine] <- prompt:
				case <-callbackCtx.Done():
					return nil, callbackCtx.Err()
				}
				select {
				case answer := <-prompt.reply:
					return answer, nil
				case <-callbackCtx.Done():
					return nil, callbackCtx.Err()
				}
			},
		})
	}
	for round, allowed := range engines {
		threads, turns := map[runtimeidentity.Engine]string{}, map[runtimeidentity.Engine]string{}
		paths := map[runtimeidentity.Engine]string{}
		events := map[runtimeidentity.Engine]*codex.EventSubscription{}
		pending := map[runtimeidentity.Engine]runtimeApprovalPrompt{}
		for _, engine := range engines {
			decision := "decline"
			if engine == allowed {
				decision = "accept"
			}
			params := map[string]any{"cwd": filepath.Join(f.root, "project"), "sandbox": "danger-full-access", "approvalPolicy": "on-request"}
			input := "PARALLEL_CLAUDE_" + decision
			paths[engine] = filepath.Join(f.root, "project", "parallel-claude-"+decision+".txt")
			if engine == runtimeidentity.Codex {
				params["approvalPolicy"] = "untrusted"
				params["config"] = map[string]any{"features.unified_exec": false}
				input = "CODEX_APPROVAL_command-" + decision
				paths[engine] = filepath.Join(f.root, "project", "command-"+decision+".txt")
			}
			thread := readSessionThread(t, ctx, clients[engine], "thread/start", params)
			threads[engine] = thread.ID
			events[engine] = clients[engine].Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
			t.Cleanup(events[engine].Close)
			var started struct{ Turn struct{ ID string } }
			require.NoError(t, clients[engine].Call(ctx, "turn/start", map[string]any{"threadId": thread.ID,
				"clientUserMessageId": fmt.Sprintf("parallel-shared-submit-%d", round), "input": []map[string]string{{"type": "text", "text": input}}}, &started))
			turns[engine] = started.Turn.ID
		}
		for _, engine := range engines {
			select {
			case pending[engine] = <-prompts[engine]:
			case <-ctx.Done():
				t.Fatal("未同时收到两个原生审批")
			}
			method := "item/fileChange/requestApproval"
			if engine == runtimeidentity.Codex {
				method = "item/commandExecution/requestApproval"
			}
			require.Equal(t, method, pending[engine].request.Method)
			var identity struct{ ThreadID, TurnID string }
			require.NoError(t, json.Unmarshal(pending[engine].request.Params, &identity))
			require.Equal(t, threads[engine], identity.ThreadID)
			require.Equal(t, turns[engine], identity.TurnID)
			_, err := os.Stat(paths[engine])
			require.True(t, os.IsNotExist(err), "两个审批挂起时均不能写文件")
		}
		pending[allowed].reply <- map[string]string{"decision": "accept"}
		waitIsolationTurnCompleted(t, ctx, events[allowed], threads[allowed], turns[allowed])
		content, err := os.ReadFile(paths[allowed])
		require.NoError(t, err)
		expected := "accept"
		if allowed == runtimeidentity.Codex {
			expected = "command-accept\n"
		}
		require.Equal(t, expected, string(content))
		other := runtimeidentity.Codex
		if allowed == runtimeidentity.Codex {
			other = runtimeidentity.Claude
		}
		_, err = os.Stat(paths[other])
		require.True(t, os.IsNotExist(err), "允许一个引擎不能允许另一个引擎的待办")
		if other == runtimeidentity.Claude {
			require.Equal(t, int64(round*2+1), f.claudeCalls.Load())
		} else {
			require.Equal(t, int64(round*2+1), f.codex.calls.Load())
		}
		pending[other].reply <- map[string]string{"decision": "decline"}
		waitIsolationTurnCompleted(t, ctx, events[other], threads[other], turns[other])
		_, err = os.Stat(paths[other])
		require.True(t, os.IsNotExist(err), "另一引擎拒绝必须保持零副作用")
		for _, engine := range engines {
			waitSessionTurn(t, ctx, clients[engine], threads[engine], turns[engine])
			events[engine].Close()
		}
		require.Equal(t, int64((round+1)*2), f.codex.calls.Load())
		require.Equal(t, int64((round+1)*2), f.claudeCalls.Load())
	}
}
