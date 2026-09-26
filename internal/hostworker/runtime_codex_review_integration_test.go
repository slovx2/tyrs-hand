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
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeCodexReviewRealSSH(t *testing.T) { testRuntimeRegistryRealSSH(t, "codex-review") }

type runtimeCodexReviewFixture struct {
	root     string
	calls    atomic.Int64
	contents map[string]string
}

func newRuntimeCodexReviewFixture(root string) *runtimeCodexReviewFixture {
	return &runtimeCodexReviewFixture{root: root, contents: map[string]string{"inline": "CODEX_READ_INLINE_" + rand.Text(), "detached": "CODEX_READ_DETACHED_" + rand.Text()}}
}

func (f *runtimeCodexReviewFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	t.Helper()
	require.Equal(t, "/v1/responses", request.URL.Path)
	step := f.calls.Add(1)
	require.LessOrEqual(t, step, int64(4), "审查管理和恢复不能额外请求模型")
	mode := "inline"
	if step > 2 {
		mode = "detached"
	}
	id := "codex-review-" + mode
	var parsed struct {
		Tools []struct{ Name string }
		Input []struct {
			Type   string
			CallID string `json:"call_id"`
			Output string
		}
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.Contains(t, string(body), "REVIEW_CODEX_"+mode)
	if step%2 == 0 {
		found := false
		for _, item := range parsed.Input {
			if item.Type == "function_call_output" && item.CallID == id {
				require.Contains(t, item.Output, f.contents[mode], "真实shell读取内容必须回到模型")
				found = true
			}
		}
		require.True(t, found)
		result, err := json.Marshal(map[string]any{"findings": []any{}, "overall_correctness": "patch is correct", "overall_explanation": "REVIEW_CODEX_FINDING_" + mode, "overall_confidence_score": 1})
		require.NoError(t, err)
		runtimeTextModel(w, request, string(result), id+"-done")
		return
	}
	require.NotContains(t, string(body), f.contents[mode], "读取前模型不得预知本轮随机文件内容")
	declared := false
	for _, tool := range parsed.Tools {
		declared = declared || tool.Name == "exec_command"
	}
	require.True(t, declared, "只调用真实CLI声明的shell工具")
	args, err := json.Marshal(map[string]any{"cmd": "cat review-codex-target.txt", "workdir": filepath.Join(f.root, "project"), "login": false})
	require.NoError(t, err)
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		data, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
		require.NoError(t, err)
	}
	event("response.created", map[string]any{"response": map[string]any{"id": id}})
	event("response.output_item.done", map[string]any{"item": map[string]any{"type": "function_call", "name": "exec_command", "call_id": id, "arguments": string(args)}})
	event("response.completed", map[string]any{"response": map[string]any{"id": id, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}})
}

func verifyRuntimeCodexReview(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connection *ssh.Client, fixture *runtimeCodexReviewFixture, root string) {
	t.Helper()
	claudeGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	client, trace := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{})
	var parent runtimeReviewState
	// 外层沙箱已禁止公网；本专项仅验证审查和实际读取，不宣称覆盖只读OS沙箱。
	require.NoError(t, client.Call(ctx, "thread/start", map[string]any{"cwd": filepath.Join(root, "project"), "approvalPolicy": "never", "sandbox": "danger-full-access"}, &parent))
	require.JSONEq(t, `"never"`, string(parent.ApprovalPolicy))
	path := filepath.Join(root, "project", "review-codex-target.txt")
	type record struct {
		threadID, turnID, mode string
		state                  runtimeReviewState
	}
	var completed []record
	for _, mode := range []string{"inline", "detached"} {
		t.Logf("真实SSH Codex审查：%s", mode)
		require.NoError(t, os.WriteFile(path, []byte(fixture.contents[mode]), 0o600))
		var before runtimeReviewThread
		if mode == "detached" {
			before = runtimeReviewRead(t, ctx, client, parent.Thread.ID)
		}
		events := client.Subscribe(codex.ThreadFilter{})
		var started struct {
			Turn           struct{ ID, Status string }
			ReviewThreadID string
		}
		require.NoError(t, client.Call(ctx, "review/start", map[string]any{"threadId": parent.Thread.ID, "delivery": mode, "target": map[string]string{"type": "custom", "instructions": "REVIEW_CODEX_" + mode + ": read " + path + " and report the finding"}}, &started))
		require.Equal(t, "inProgress", started.Turn.Status)
		if mode == "inline" {
			require.Equal(t, parent.Thread.ID, started.ReviewThreadID)
		} else {
			require.NotEqual(t, parent.Thread.ID, started.ReviewThreadID)
		}
		runtimeReviewWait(t, ctx, events, started.ReviewThreadID, started.Turn.ID)
		events.Close()
		runtimeCodexReviewHistory(t, runtimeReviewRead(t, ctx, client, started.ReviewThreadID), started.Turn.ID, mode)
		runtimeCodexReviewEvents(t, trace, started.ReviewThreadID, started.Turn.ID, mode)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, fixture.contents[mode], string(content), "本次真实审查不能修改目标文件")
		if mode == "detached" {
			after := runtimeReviewRead(t, ctx, client, parent.Thread.ID)
			require.Equal(t, before.Turns, after.Turns, "独立审查不能改变父会话历史")
		}
		runtimeReviewPermissions(t, parent, runtimeReviewResume(t, ctx, client, parent.Thread.ID))
		state := runtimeReviewResume(t, ctx, client, started.ReviewThreadID)
		if mode == "detached" {
			// 原生独立审查使用只读策略，不能要求扩大为父会话的完全访问。
			require.JSONEq(t, `"never"`, string(state.ApprovalPolicy))
			require.JSONEq(t, `{"type":"readOnly","networkAccess":false}`, string(state.Sandbox))
			require.Equal(t, parent.Cwd, state.Cwd)
			require.Equal(t, parent.Model, state.Model)
			require.Equal(t, parent.ModelProvider, state.ModelProvider)
		} else {
			runtimeReviewPermissions(t, parent, state)
		}
		completed = append(completed, record{started.ReviewThreadID, started.Turn.ID, mode, state})
	}
	require.Equal(t, int64(4), fixture.calls.Load())
	trace.expectClose("runtime-restart")
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	require.Equal(t, claudeGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
	resumed, _ := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{})
	for _, record := range completed {
		runtimeReviewPermissions(t, record.state, runtimeReviewResume(t, ctx, resumed, record.threadID))
		runtimeCodexReviewHistory(t, runtimeReviewRead(t, ctx, resumed, record.threadID), record.turnID, record.mode)
	}
	require.Len(t, runtimeReviewRead(t, ctx, resumed, parent.Thread.ID).Turns, 1)
	require.Equal(t, int64(4), fixture.calls.Load(), "重启恢复不得重放工具或模型请求")
}

func runtimeCodexReviewHistory(t *testing.T, thread runtimeReviewThread, turnID, mode string) {
	t.Helper()
	found := 0
	for _, turn := range thread.Turns {
		if turn.ID != turnID {
			continue
		}
		found++
		require.Equal(t, "completed", turn.Status)
		counts := map[string]int{}
		for _, item := range turn.Items {
			var value struct{ Type, Review string }
			require.NoError(t, json.Unmarshal(item, &value))
			counts[value.Type]++
			if value.Type == "exitedReviewMode" {
				require.Contains(t, value.Review, "REVIEW_CODEX_FINDING_"+mode)
			}
		}
		require.Equal(t, 1, counts["enteredReviewMode"])
		require.Equal(t, 1, counts["exitedReviewMode"])
	}
	require.Equal(t, 1, found)
}

func runtimeCodexReviewEvents(t *testing.T, trace *protocolTraceTransport, threadID, turnID, mode string) {
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
			Turn             struct{ ID string }
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
			require.Contains(t, value.Item.Review, "REVIEW_CODEX_FINDING_"+mode)
		}
		key := method + ":" + value.Item.Type
		indices[key] = append(indices[key], index)
	}
	last := -1
	for _, key := range []string{"turn/started", "item/started:enteredReviewMode", "item/completed:enteredReviewMode", "item/started:exitedReviewMode", "item/completed:exitedReviewMode", "turn/completed"} {
		require.Len(t, indices[key], 1, "审查事件必须恰好一次：%s", key)
		require.Greater(t, indices[key][0], last, "审查进入、退出和终态必须有序")
		last = indices[key][0]
	}
}
