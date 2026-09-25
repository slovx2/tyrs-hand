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
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type runtimePlanFixture struct {
	root  string
	calls atomic.Int64
}

func (f *runtimePlanFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	step := f.calls.Add(1)
	tool := func(name, id string, input map[string]any) map[string]any {
		return map[string]any{"type": "tool_use", "name": name, "id": id, "input": input}
	}
	var blocks []map[string]any
	switch step {
	case 1:
		blocks = []map[string]any{tool("AskUserQuestion", "toolu_color", map[string]any{"questions": []map[string]any{{
			"question": "Which color?", "header": "Color", "multiSelect": false, "options": []map[string]any{
				{"label": "Blue", "description": "Blue"}, {"label": "Red", "description": "Red"},
			},
		}}})}
	case 2:
		require.Contains(t, string(body), "Blue", "用户答案必须回到真实 SDK 模型上下文")
		blocks = []map[string]any{{"type": "text", "text": "SSH_PLAN：确认后写入蓝色文件。"}, tool("ExitPlanMode", "toolu_exit", map[string]any{})}
	case 3:
		blocks = []map[string]any{tool("Write", "toolu_plan_write", map[string]any{"file_path": filepath.Join(f.root, "plan-result.txt"), "content": "Blue"})}
	case 5:
		blocks = []map[string]any{tool("Bash", "toolu_command", map[string]any{"command": "printf approved >> " + filepath.Join(f.root, "command-result.txt"), "description": "写入审批结果"})}
	case 7:
		blocks = []map[string]any{tool("Write", "toolu_decline", map[string]any{"file_path": filepath.Join(f.root, "denied.txt"), "content": "必须拒绝"})}
	case 4, 6, 8:
		runtimeTextModel(w, request, "SSH_PLAN_DONE", fmt.Sprintf("plan-%d", step))
		return
	default:
		t.Error("计划或审批发生重复模型请求")
		http.Error(w, "unexpected model call", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
	}
	event("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("msg_plan_%d", step), "type": "message", "role": "assistant", "model": "claude-config-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	for index, block := range blocks {
		delta := map[string]any{"type": "text_delta", "text": block["text"]}
		initial := map[string]any{"type": "text", "text": ""}
		if block["type"] == "tool_use" {
			data, err := json.Marshal(block["input"])
			require.NoError(t, err)
			delta = map[string]any{"type": "input_json_delta", "partial_json": string(data)}
			initial = map[string]any{"type": "tool_use", "id": block["id"], "name": block["name"], "input": map[string]any{}}
		}
		event("content_block_start", map[string]any{"index": index, "content_block": initial})
		event("content_block_delta", map[string]any{"index": index, "delta": delta})
		event("content_block_stop", map[string]any{"index": index})
	}
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 10}})
	event("message_stop", map[string]any{})
}

// PLAN-003：协议客户端经双 SSH 接力，Hub 仲裁计划提问和命令/文件审批。
func verifyRuntimePlanApproval(t *testing.T, ctx context.Context, connection *ssh.Client, fixture *runtimePlanFixture) {
	t.Helper()
	var mu sync.Mutex
	arrived := map[string]int{}
	barriers := map[string]chan struct{}{}
	var count atomic.Int64
	clients := make([]*codex.SocketClient, 0, 2)
	for _, name := range []string{"desktop", "phone"} {
		client := connectRuntimeSSHWithOptions(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{ClientName: name,
			ServerRequestHandler: func(callbackCtx context.Context, req codex.ServerRequest) (any, error) {
				var params struct {
					ItemID    string
					Questions []struct{ ID string }
				}
				if err := json.Unmarshal(req.Params, &params); err != nil {
					return nil, err
				}
				mu.Lock()
				if barriers[params.ItemID] == nil {
					barriers[params.ItemID] = make(chan struct{})
				}
				ready := barriers[params.ItemID]
				arrived[params.ItemID]++
				if arrived[params.ItemID] == 2 {
					close(ready)
				}
				mu.Unlock()
				select {
				case <-ready:
				case <-callbackCtx.Done():
					return nil, callbackCtx.Err()
				}
				count.Add(1)
				switch req.Method {
				case "item/tool/requestUserInput":
					answer := "Blue"
					if params.Questions[0].ID == "execute_plan" {
						answer = "执行计划"
					}
					return map[string]any{"answers": map[string]any{params.Questions[0].ID: map[string]any{"answers": []string{answer}}}}, nil
				case "item/commandExecution/requestApproval":
					return map[string]any{"decision": "accept"}, nil
				case "item/fileChange/requestApproval":
					return map[string]any{"decision": "decline"}, nil
				default:
					return nil, fmt.Errorf("未预期的审批 %s", req.Method)
				}
			},
		})
		clients = append(clients, client)
	}
	thread := readSessionThread(t, ctx, clients[0], "thread/start", map[string]any{"cwd": fixture.root, "permissions": ":danger-full-access"})
	readSessionThread(t, ctx, clients[1], "thread/resume", map[string]any{"threadId": thread.ID})
	for index := range 3 {
		params := map[string]any{"threadId": thread.ID, "input": []map[string]any{{"type": "text", "text": fmt.Sprintf("SSH_PLAN_%d", index)}}}
		if index == 0 {
			params["collaborationMode"] = map[string]any{"mode": "plan", "settings": map[string]any{"model": "claude-config-model", "reasoning_effort": nil, "developer_instructions": nil}}
		} else {
			params["approvalPolicy"] = "on-request"
		}
		var started struct{ Turn struct{ ID string } }
		client := clients[index%2]
		require.NoError(t, client.Call(ctx, "turn/start", params, &started))
		waitSessionTurn(t, ctx, client, thread.ID, started.Turn.ID)
	}
	require.Equal(t, int64(8), count.Load(), "两端都必须收到两次提问及两次审批")
	data, err := os.ReadFile(filepath.Join(fixture.root, "plan-result.txt"))
	require.NoError(t, err)
	require.Equal(t, "Blue", string(data))
	data, err = os.ReadFile(filepath.Join(fixture.root, "command-result.txt"))
	require.NoError(t, err)
	require.Equal(t, "approved", string(data), "两个回答只能执行一次命令")
	_, err = os.Stat(filepath.Join(fixture.root, "denied.txt"))
	require.True(t, os.IsNotExist(err), "被拒绝的文件不能写入")
	var history map[string]any
	require.NoError(t, clients[1].Call(ctx, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true}, &history))
	serialized, err := json.Marshal(history)
	require.NoError(t, err)
	require.Contains(t, string(serialized), "SSH_PLAN：", "计划必须能从另一客户端历史恢复")
}
