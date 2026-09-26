//go:build integration

package hostworker

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeReviewRealSSH(t *testing.T) { testRuntimeRegistryRealSSH(t, "review") }

type runtimeReviewFixture struct {
	root     string
	calls    atomic.Int64
	contents map[string]string
	outputs  sync.Map
}

func newRuntimeReviewFixture(root string) *runtimeReviewFixture {
	return &runtimeReviewFixture{root: root, contents: map[string]string{"inline": "READ_INLINE_" + rand.Text(), "detached": "READ_DETACHED_" + rand.Text()}}
}

func (f *runtimeReviewFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	t.Helper()
	step := f.calls.Add(1)
	require.LessOrEqual(t, step, int64(4), "审查分叉、恢复和管理不能额外请求模型")
	mode := "inline"
	if step > 2 {
		mode = "detached"
	}
	toolID := "toolu_review_ssh_" + mode
	var parsed struct {
		Tools    []struct{ Name string }
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.True(t, strings.Contains(string(body), "REVIEW_SSH_"+mode), "真实审查指令必须到达模型")
	if step%2 == 0 {
		found := false
		for _, message := range parsed.Messages {
			var blocks []struct {
				Type      string
				ToolUseID string `json:"tool_use_id"`
				IsError   bool   `json:"is_error"`
				Content   json.RawMessage
			}
			if json.Unmarshal(message.Content, &blocks) != nil {
				continue
			}
			for _, block := range blocks {
				if block.Type != "tool_result" || block.ToolUseID != toolID {
					continue
				}
				require.False(t, block.IsError)
				require.True(t, strings.Contains(string(block.Content), f.contents[mode]), "必须读到本轮刚写入的随机文件内容，不能复用父历史")
				found = true
			}
		}
		require.True(t, found, "真实Read结果必须回到模型")
		_, repeated := f.outputs.LoadOrStore(mode, true)
		require.False(t, repeated)
		runtimeTextModel(w, request, "REVIEW_SSH_FINDING_"+mode, "review-ssh-"+mode)
		return
	}
	require.False(t, strings.Contains(string(body), f.contents[mode]), "读取前模型不得预先获得随机文件内容")
	declared := false
	for _, tool := range parsed.Tools {
		declared = declared || tool.Name == "Read"
	}
	require.True(t, declared, "只能调用真实CLI工具目录中的Read")
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		data, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
		require.NoError(t, err)
	}
	event("message_start", map[string]any{"message": map[string]any{"id": "msg_review_" + mode, "type": "message", "role": "assistant", "model": "claude-config-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": toolID, "name": "Read", "input": map[string]any{}}})
	args, err := json.Marshal(map[string]string{"file_path": filepath.Join(f.root, "project", "review-target.txt")})
	require.NoError(t, err)
	event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 5}})
	event("message_stop", map[string]any{})
}

type runtimeReviewTurn struct {
	ID, Status string
	Items      []json.RawMessage
}
type runtimeReviewThread struct {
	ID, Cwd, ForkedFromID string
	Turns                 []runtimeReviewTurn
}
type runtimeReviewState struct {
	Thread                    runtimeReviewThread
	ApprovalPolicy, Sandbox   json.RawMessage
	Model, ModelProvider, Cwd string
}

func runtimeReviewRead(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID string) runtimeReviewThread {
	t.Helper()
	var result struct{ Thread runtimeReviewThread }
	require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &result))
	require.Equal(t, threadID, result.Thread.ID)
	return result.Thread
}

func runtimeReviewResume(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID string) runtimeReviewState {
	t.Helper()
	var state runtimeReviewState
	require.NoError(t, client.Call(ctx, "thread/resume", map[string]string{"threadId": threadID}, &state))
	require.Equal(t, threadID, state.Thread.ID)
	return state
}

func runtimeReviewPermissions(t *testing.T, before, after runtimeReviewState) {
	t.Helper()
	require.JSONEq(t, string(before.ApprovalPolicy), string(after.ApprovalPolicy))
	require.JSONEq(t, string(before.Sandbox), string(after.Sandbox))
	require.Equal(t, before.Cwd, after.Cwd)
	require.Equal(t, before.Model, after.Model)
	require.Equal(t, before.ModelProvider, after.ModelProvider)
}

func verifyRuntimeReview(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connection *ssh.Client, fixture *runtimeReviewFixture, root string) {
	t.Helper()
	codexGeneration := registry.entries[runtimeidentity.Codex].Runtime.Generation()
	client, trace := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{})
	var parent runtimeReviewState
	require.NoError(t, client.Call(ctx, "thread/start", map[string]any{"cwd": filepath.Join(root, "project"), "approvalPolicy": "never", "sandbox": "read-only"}, &parent))
	require.JSONEq(t, `"never"`, string(parent.ApprovalPolicy))
	var sandbox struct{ Type string }
	require.NoError(t, json.Unmarshal(parent.Sandbox, &sandbox))
	require.Equal(t, "readOnly", sandbox.Type)
	path := filepath.Join(root, "project", "review-target.txt")
	type record struct{ threadID, turnID, mode string }
	var completed []record
	for _, mode := range []string{"inline", "detached"} {
		t.Logf("真实SSH审查：%s", mode)
		require.NoError(t, os.WriteFile(path, []byte(fixture.contents[mode]), 0o600))
		before := runtimeReviewRead(t, ctx, client, parent.Thread.ID)
		events := client.Subscribe(codex.ThreadFilter{})
		var started struct {
			Turn           struct{ ID, Status string }
			ReviewThreadID string
		}
		require.NoError(t, client.Call(ctx, "review/start", map[string]any{"threadId": parent.Thread.ID, "delivery": mode, "target": map[string]string{"type": "custom", "instructions": "REVIEW_SSH_" + mode + ": read " + path + " and report the finding"}}, &started))
		require.Equal(t, "inProgress", started.Turn.Status)
		if mode == "inline" {
			require.Equal(t, parent.Thread.ID, started.ReviewThreadID)
		} else {
			require.NotEqual(t, parent.Thread.ID, started.ReviewThreadID)
		}
		runtimeReviewWait(t, ctx, events, started.ReviewThreadID, started.Turn.ID)
		events.Close()
		history := runtimeReviewRead(t, ctx, client, started.ReviewThreadID)
		runtimeReviewHistory(t, history, started.Turn.ID, mode)
		runtimeReviewEvents(t, trace, started.ReviewThreadID, started.Turn.ID, mode)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, fixture.contents[mode], string(content), "只读审查不能修改真实目标文件")
		if mode == "detached" {
			require.Equal(t, parent.Thread.ID, history.ForkedFromID)
			after := runtimeReviewRead(t, ctx, client, parent.Thread.ID)
			left, err := json.Marshal(before.Turns)
			require.NoError(t, err)
			right, err := json.Marshal(after.Turns)
			require.NoError(t, err)
			require.JSONEq(t, string(left), string(right), "独立审查不得改变父会话历史")
		}
		runtimeReviewPermissions(t, parent, runtimeReviewResume(t, ctx, client, parent.Thread.ID))
		runtimeReviewPermissions(t, parent, runtimeReviewResume(t, ctx, client, started.ReviewThreadID))
		completed = append(completed, record{started.ReviewThreadID, started.Turn.ID, mode})
	}
	require.Equal(t, int64(4), fixture.calls.Load())
	trace.expectClose("runtime-restart")
	require.NoError(t, registry.Restart(runtimeidentity.Claude))
	require.Equal(t, codexGeneration, registry.entries[runtimeidentity.Codex].Runtime.Generation())
	resumed, _ := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{})
	for _, record := range completed {
		runtimeReviewPermissions(t, parent, runtimeReviewResume(t, ctx, resumed, record.threadID))
		runtimeReviewHistory(t, runtimeReviewRead(t, ctx, resumed, record.threadID), record.turnID, record.mode)
		_, returned := fixture.outputs.Load(record.mode)
		require.True(t, returned)
	}
	require.Len(t, runtimeReviewRead(t, ctx, resumed, parent.Thread.ID).Turns, 1)
	require.Equal(t, int64(4), fixture.calls.Load(), "恢复真实审查历史不能重放Read或请求模型")
}

func runtimeReviewWait(t *testing.T, ctx context.Context, events *codex.EventSubscription, threadID, turnID string) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("真实SSH审查未完成")
		case event, ok := <-events.Events():
			require.True(t, ok)
			if event.Method != "turn/completed" {
				continue
			}
			var value struct {
				ThreadID string
				Turn     struct {
					ID, Status string
					Error      any
				}
			}
			require.NoError(t, json.Unmarshal(event.Params, &value))
			if value.ThreadID != threadID || value.Turn.ID != turnID {
				continue
			}
			require.Equal(t, "completed", value.Turn.Status)
			require.Nil(t, value.Turn.Error)
			return
		}
	}
}

func runtimeReviewHistory(t *testing.T, thread runtimeReviewThread, turnID, mode string) {
	t.Helper()
	found := 0
	for _, turn := range thread.Turns {
		if turn.ID != turnID {
			continue
		}
		found++
		require.Equal(t, "completed", turn.Status)
		counts := map[string]int{}
		reply := false
		for _, item := range turn.Items {
			var value struct{ Type, Review, Text string }
			require.NoError(t, json.Unmarshal(item, &value))
			counts[value.Type]++
			if value.Type == "exitedReviewMode" {
				require.Equal(t, "REVIEW_SSH_FINDING_"+mode, value.Review)
			}
			if value.Type == "agentMessage" && value.Text == "REVIEW_SSH_FINDING_"+mode {
				reply = true
			}
		}
		require.Equal(t, 1, counts["enteredReviewMode"])
		require.Equal(t, 1, counts["exitedReviewMode"])
		require.True(t, reply, "真实原生审查回复必须保存在历史中")
	}
	require.Equal(t, 1, found)
}

func runtimeReviewEvents(t *testing.T, trace *protocolTraceTransport, threadID, turnID, mode string) {
	t.Helper()
	trace.mu.Lock()
	defer trace.mu.Unlock()
	indices := map[string][]int{}
	for index, message := range trace.messages {
		if message["direction"] != "server" {
			continue
		}
		method, _ := message["method"].(string)
		if method != "item/started" && method != "item/completed" && method != "turn/started" && method != "turn/completed" {
			continue
		}
		data, err := json.Marshal(message["params"])
		require.NoError(t, err)
		var value struct {
			ThreadID, TurnID string
			Item             struct{ Type, Review string }
			Turn             struct{ ID, Status string }
		}
		require.NoError(t, json.Unmarshal(data, &value))
		if value.ThreadID != threadID || (value.TurnID != turnID && value.Turn.ID != turnID) {
			continue
		}
		if strings.HasPrefix(method, "turn/") {
			indices[method] = append(indices[method], index)
			continue
		}
		if value.Item.Type != "enteredReviewMode" && value.Item.Type != "exitedReviewMode" {
			continue
		}
		if value.Item.Type == "exitedReviewMode" {
			require.Equal(t, "REVIEW_SSH_FINDING_"+mode, value.Item.Review)
		}
		key := method + ":" + value.Item.Type
		indices[key] = append(indices[key], index)
	}
	order := []string{"turn/started", "item/started:enteredReviewMode", "item/completed:enteredReviewMode", "item/started:exitedReviewMode", "item/completed:exitedReviewMode", "turn/completed"}
	last := -1
	for _, key := range order {
		require.Len(t, indices[key], 1, "审查事件必须恰好一次：%s", key)
		require.Greater(t, indices[key][0], last, "审查进入、退出、回合终态必须有序")
		last = indices[key][0]
	}
}
