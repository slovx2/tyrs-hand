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
	"github.com/stretchr/testify/require"
)

func TestRuntimeGoalExecutionRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "goal-execution")
}

type runtimeGoalExecutionFixture struct {
	root, secret string
	calls        atomic.Int64
}

func requireGoalToolResult(t *testing.T, body []byte, toolID, expected string) {
	t.Helper()
	var request struct {
		Messages []struct{ Content json.RawMessage }
	}
	require.NoError(t, json.Unmarshal(body, &request))
	for _, message := range request.Messages {
		var blocks []struct {
			Type      string
			ToolUseID string `json:"tool_use_id"`
			Content   json.RawMessage
		}
		// 消息内容允许纯字符串；只检查真实工具结果块，不将旧工具参数算作结果。
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type == "tool_result" && block.ToolUseID == toolID {
				require.Contains(t, string(block.Content), expected, "真实 SDK 工具结果必须回到模型上下文")
				return
			}
		}
	}
	t.Fatalf("真实模型请求缺少工具结果 %s", toolID)
}

func (f *runtimeGoalExecutionFixture) model(t *testing.T, w http.ResponseWriter, body []byte) {
	step := f.calls.Add(1)
	tool := func(name, id string, input map[string]any) map[string]any {
		return map[string]any{"type": "tool_use", "name": name, "id": id, "input": input}
	}
	var block map[string]any
	switch step {
	case 1:
		require.Contains(t, string(body), "GOAL_SSH_COMPLETE", "目标设置必须自动发起首个真实回合")
		block = tool("Write", "goal_write", map[string]any{"file_path": filepath.Join(f.root, "goal-complete.txt"), "content": f.secret})
	case 2:
		requireGoalToolResult(t, body, "goal_write", "goal-complete.txt")
		block = map[string]any{"type": "text", "text": "文件已写入，后续回合读取验证。"}
	case 3:
		require.Contains(t, string(body), "GOAL_SSH_COMPLETE", "目标未完成时必须自动续跑")
		block = tool("Read", "goal_read", map[string]any{"file_path": filepath.Join(f.root, "goal-complete.txt")})
	case 4:
		requireGoalToolResult(t, body, "goal_read", f.secret)
		require.Contains(t, string(body), "mcp__tyrs_goal__update_goal")
		block = tool("mcp__tyrs_goal__update_goal", "goal_complete", map[string]any{"status": "complete"})
	case 5:
		requireGoalToolResult(t, body, "goal_complete", "complete")
		block = map[string]any{"type": "text", "text": "GOAL_SSH_COMPLETE_DONE"}
	case 6:
		require.Contains(t, string(body), "GOAL_SSH_BUDGET")
		block = tool("Write", "goal_budget_write", map[string]any{"file_path": filepath.Join(f.root, "goal-budget.txt"), "content": f.secret + "-budget"})
	case 7:
		requireGoalToolResult(t, body, "goal_budget_write", "goal-budget.txt")
		block = map[string]any{"type": "text", "text": "GOAL_SSH_BUDGET_DONE"}
	default:
		t.Error("目标完成或预算到达后发生额外模型请求")
		http.Error(w, "unexpected model call", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, data map[string]any) {
		data["type"] = kind
		encoded, err := json.Marshal(data)
		require.NoError(t, err)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
	}
	event("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("msg_goal_%d", step), "type": "message", "role": "assistant", "model": "claude-config-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 100, "output_tokens": 1}}})
	initial := map[string]any{"type": "text", "text": ""}
	delta := map[string]any{"type": "text_delta", "text": block["text"]}
	stopReason := "end_turn"
	if block["type"] == "tool_use" {
		encoded, err := json.Marshal(block["input"])
		require.NoError(t, err)
		initial = map[string]any{"type": "tool_use", "id": block["id"], "name": block["name"], "input": map[string]any{}}
		delta = map[string]any{"type": "input_json_delta", "partial_json": string(encoded)}
		stopReason = "tool_use"
	}
	event("content_block_start", map[string]any{"index": 0, "content_block": initial})
	event("content_block_delta", map[string]any{"index": 0, "delta": delta})
	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 20}})
	event("message_stop", map[string]any{})
}

func waitRuntimeGoalExecution(t *testing.T, ctx context.Context, events *codex.EventSubscription, status string, expectedTurns int) {
	t.Helper()
	started, completed := map[string]bool{}, map[string]bool{}
	goalReached := false
	for !goalReached || len(completed) < expectedTurns {
		select {
		case event, ok := <-events.Events():
			require.True(t, ok, "目标完成之前不能断开真实 SSH 事件流")
			var params struct {
				Turn struct{ ID, Status string }
				Goal *nativePausedGoal
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			switch event.Method {
			case "thread/goal/updated":
				if params.Goal != nil && params.Goal.Status == status {
					goalReached = true
				}
			case "turn/started":
				require.False(t, started[params.Turn.ID], "每个自动回合只能启动一次")
				started[params.Turn.ID] = true
			case "turn/completed":
				require.Equal(t, "completed", params.Turn.Status)
				completed[params.Turn.ID] = true
			}
		case <-ctx.Done():
			t.Fatalf("目标未到达 %s 或自动回合未结束: %v", status, ctx.Err())
		}
	}
	require.Len(t, started, expectedTurns)
	require.Equal(t, started, completed, "全部自动回合必须有真实结束事件")
}

// GOAL-005：不提交 turn/start，由目标自动触发两轮真实 Write/Read/complete 与独立软预算停止。
func verifyRuntimeGoalExecution(t *testing.T, ctx context.Context, client *codex.SocketClient, fixture *runtimeGoalExecutionFixture) {
	t.Helper()
	for _, scenario := range []struct {
		name, objective, status, file, content string
		budget, tokens, calls                  int64
		turns                                  int
	}{
		{"complete", "GOAL_SSH_COMPLETE", "complete", "goal-complete.txt", fixture.secret, 2000, 480, 5, 2},
		{"budget", "GOAL_SSH_BUDGET", "budgetLimited", "goal-budget.txt", fixture.secret + "-budget", 1, 240, 7, 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": fixture.root, "approvalPolicy": "never", "sandbox": "danger-full-access"})
			events := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
			defer events.Close()
			created := nativeMetadataCall[nativeGoalResponse](t, ctx, client, "thread/goal/set", map[string]any{"threadId": thread.ID, "objective": scenario.objective, "tokenBudget": scenario.budget})
			require.NotNil(t, created.Goal)
			require.Equal(t, scenario.objective, created.Goal.Objective)
			waitRuntimeGoalExecution(t, ctx, events, scenario.status, scenario.turns)
			data, err := os.ReadFile(filepath.Join(fixture.root, scenario.file))
			require.NoError(t, err)
			require.Equal(t, scenario.content, string(data), "必须验证真实 CLI 文件副作用")
			goal := nativeMetadataCall[nativeGoalResponse](t, ctx, client, "thread/goal/get", map[string]any{"threadId": thread.ID}).Goal
			require.NotNil(t, goal)
			require.Equal(t, scenario.status, goal.Status)
			require.Equal(t, scenario.tokens, goal.TokensUsed, "只累计目标活动期间的真实模型用量")
			require.Equal(t, scenario.budget, goal.TokenBudget)
			history := readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true})
			require.Len(t, history.Turns, scenario.turns)
			for _, turn := range history.Turns {
				require.Equal(t, "completed", turn.Status)
			}
			time.Sleep(350 * time.Millisecond)
			require.Equal(t, scenario.calls, fixture.calls.Load(), "完成或预算耗尽后不得继续请求模型")
			if scenario.status == "budgetLimited" {
				updated := nativeMetadataCall[nativeGoalResponse](t, ctx, client, "thread/goal/set", map[string]any{"threadId": thread.ID, "objective": "SSH 暂停保留实际账本", "status": "paused", "tokenBudget": nil})
				require.NotNil(t, updated.Goal)
				require.Equal(t, goal.TokensUsed, updated.Goal.TokensUsed)
				require.Equal(t, goal.CreatedAt, updated.Goal.CreatedAt)
			}
		})
	}
}
