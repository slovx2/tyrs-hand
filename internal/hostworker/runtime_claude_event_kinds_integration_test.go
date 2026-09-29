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
	"sync"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// 只脚本化模型 SSE；条目、增量、用量、终态和历史均由真实适配器、SDK/CLI 与工具执行产生。
type runtimeClaudeEventKindsFixture struct {
	project string
	mu      sync.Mutex
	steps   map[string]int
}

type claudeSSEBlock struct {
	kind   string // thinking / text / tool_use
	deltas []string
	tool   string
	input  map[string]any
}

func writeClaudeSSE(t *testing.T, w http.ResponseWriter, id string, blocks []claudeSSEBlock) {
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
		require.NoError(t, err)
		w.(http.Flusher).Flush()
	}
	event("message_start", map[string]any{"message": map[string]any{"id": "msg_" + id, "type": "message", "role": "assistant",
		"model": "claude-sonnet-4-6", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	stop := "end_turn"
	for index, block := range blocks {
		switch block.kind {
		case "thinking":
			event("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "thinking", "thinking": ""}})
			for _, delta := range block.deltas {
				event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "thinking_delta", "thinking": delta}})
			}
		case "text":
			event("content_block_start", map[string]any{"index": index, "content_block": map[string]any{"type": "text", "text": ""}})
			for _, delta := range block.deltas {
				event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "text_delta", "text": delta}})
			}
		case "tool_use":
			stop = "tool_use"
			encoded, err := json.Marshal(block.input)
			require.NoError(t, err)
			event("content_block_start", map[string]any{"index": index, "content_block": map[string]any{
				"type": "tool_use", "id": "toolu_" + id, "name": block.tool, "input": map[string]any{}}})
			event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(encoded)}})
		}
		event("content_block_stop", map[string]any{"index": index})
	}
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": 5}})
	event("message_stop", map[string]any{})
}

func (f *runtimeClaudeEventKindsFixture) model(t *testing.T, w http.ResponseWriter, body []byte) {
	marker := "EVENTS001_FULL"
	if strings.Contains(string(body), "EVENTS001_FAIL") {
		marker = "EVENTS001_FAIL"
	}
	require.Contains(t, string(body), marker, "只接受已脚本化的事件回合")
	f.mu.Lock()
	step := f.steps[marker]
	f.steps[marker] = step + 1
	f.mu.Unlock()
	id := fmt.Sprintf("events001-%s-%d", strings.ToLower(marker), step)
	switch marker + fmt.Sprint(step) {
	case "EVENTS001_FULL0":
		writeClaudeSSE(t, w, id, []claudeSSEBlock{{kind: "thinking", deltas: []string{"EV001_THINK_A", "_B"}},
			{kind: "text", deltas: []string{"EV001_TEXT_A", "_B"}},
			{kind: "tool_use", tool: "Bash", input: map[string]any{"command": "printf EV001_CMD_OUTPUT"}}})
	case "EVENTS001_FULL1":
		require.Contains(t, lastPermissionToolResult(t, body).text, "EV001_CMD_OUTPUT", "真实命令输出必须回模")
		writeClaudeSSE(t, w, id, []claudeSSEBlock{{kind: "tool_use", tool: "Write",
			input: map[string]any{"file_path": filepath.Join(f.project, "events001.txt"), "content": "EV001_FILE"}}})
	case "EVENTS001_FULL2":
		result := lastPermissionToolResult(t, body)
		require.True(t, result.seen && !result.isError, "真实文件写入必须成功: %s", result.text)
		writeClaudeSSE(t, w, id, []claudeSSEBlock{{kind: "text", deltas: []string{"EV001_FINAL_A", "_B"}}})
	case "EVENTS001_FAIL0":
		// 回环模型拒绝请求；CLI 重试为 0，回合必须明确失败。
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"EV001_MODEL_FAILURE"}}`, http.StatusInternalServerError)
	default:
		t.Errorf("未脚本化的 EVENTS-001 模型请求 %s#%d", marker, step)
		http.Error(w, "unexpected model request", http.StatusBadRequest)
	}
}

type claudeEventKindTurn struct {
	ID, Status string
	Error      json.RawMessage
	Items      []json.RawMessage
}

// EVENTS-001：同一真 SSH 回合覆盖推理、文本、命令、文件修改条目及其增量和用量，事件顺序、配对与持久历史逐项一致；
// 模型故障回合以失败终态和错误结束，不残留进行中的条目。
func verifyRuntimeClaudeEventKinds(t *testing.T, ctx context.Context, connection *ssh.Client, fixture *runtimeClaudeEventKindsFixture) {
	t.Helper()
	require.NoError(t, os.MkdirAll(fixture.project, 0o755))
	client := connectRuntimeSSH(t, ctx, connection, runtimeidentity.Claude)
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
		"cwd": fixture.project, "approvalPolicy": "never", "sandbox": "danger-full-access"})
	history := func() []claudeEventKindTurn {
		var result struct {
			Thread struct{ Turns []claudeEventKindTurn }
		}
		require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true}, &result))
		return result.Thread.Turns
	}
	run := func(text string) (string, []codex.Event) {
		subscription := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
		defer subscription.Close()
		var started struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread.ID,
			"input": []map[string]any{{"type": "text", "text": text}}}, &started))
		var events []codex.Event
		for {
			select {
			case <-ctx.Done():
				t.Fatalf("%s 缺少终态", text)
			case event, ok := <-subscription.Events():
				require.True(t, ok)
				events = append(events, event)
				if event.Method == "turn/completed" {
					return started.Turn.ID, events
				}
			}
		}
	}

	turnID, events := run("EVENTS001_FULL")
	starts, ends := map[string]string{}, map[string]bool{}
	var completed []json.RawMessage
	kinds, deltaKinds := map[string]bool{}, map[string]bool{}
	turnStarted, tokenUsage := false, false
	for index, event := range events {
		var params struct {
			ThreadID, TurnID, ItemID string
			Item                     json.RawMessage
			Turn                     struct{ ID, Status string }
		}
		require.NoError(t, json.Unmarshal(event.Params, &params))
		if params.TurnID != "" {
			require.Equal(t, turnID, params.TurnID, "%s 必须归属本回合", event.Method)
		}
		switch {
		case event.Method == "turn/started":
			require.False(t, turnStarted, "turn/started 只能出现一次")
			require.Equal(t, turnID, params.Turn.ID)
			turnStarted = true
		case event.Method == "item/started" || event.Method == "item/completed":
			require.True(t, turnStarted, "条目事件必须晚于 turn/started")
			var item struct{ ID, Type string }
			require.NoError(t, json.Unmarshal(params.Item, &item))
			if event.Method == "item/started" {
				require.NotContains(t, starts, item.ID, "条目不能重复开始")
				starts[item.ID] = item.Type
			} else {
				require.Equal(t, starts[item.ID], item.Type, "完成条目必须与开始条目配对")
				require.False(t, ends[item.ID], "条目不能重复完成")
				ends[item.ID] = true
				kinds[item.Type] = true
				completed = append(completed, params.Item)
			}
		case strings.HasSuffix(event.Method, "Delta") || strings.HasSuffix(event.Method, "/delta"):
			require.Contains(t, starts, params.ItemID, "%s 必须晚于所属条目开始", event.Method)
			require.False(t, ends[params.ItemID], "%s 不能晚于所属条目完成", event.Method)
			deltaKinds[event.Method] = true
		case event.Method == "thread/tokenUsage/updated":
			tokenUsage = true
		case event.Method == "turn/completed":
			require.Equal(t, len(events)-1, index, "turn/completed 必须是最后一个回合事件")
			require.Equal(t, "completed", params.Turn.Status)
		}
	}
	require.Len(t, ends, len(starts), "每个开始的条目都必须完成")
	for _, kind := range []string{"userMessage", "reasoning", "agentMessage", "commandExecution", "fileChange"} {
		if kind == "userMessage" && !kinds[kind] {
			continue // 用户输入可只存在于持久历史，下面单独校验。
		}
		require.True(t, kinds[kind], "缺少 %s 条目，实际 %v", kind, kinds)
	}
	for _, method := range []string{"item/reasoning/summaryTextDelta", "item/agentMessage/delta", "item/commandExecution/outputDelta"} {
		require.True(t, deltaKinds[method], "缺少 %s 增量，实际 %v", method, deltaKinds)
	}
	require.True(t, tokenUsage, "缺少真实用量事件")
	t.Logf("回合事件 %d 条，条目种类 %v，增量种类 %v，历史条目 %d", len(events), kinds, deltaKinds, len(completed))
	data, err := os.ReadFile(filepath.Join(fixture.project, "events001.txt"))
	require.NoError(t, err)
	require.Equal(t, "EV001_FILE", string(data), "文件修改条目必须对应真实副作用")

	turns := history()
	require.Len(t, turns, 1)
	require.Equal(t, turnID, turns[0].ID)
	require.Equal(t, "completed", turns[0].Status)
	// 持久历史先保存用户输入，其余条目与实时 item/completed 逐项一致（ID、顺序与内容）。
	var input struct{ Type string }
	require.NoError(t, json.Unmarshal(turns[0].Items[0], &input))
	require.Equal(t, "userMessage", input.Type)
	live := completed
	if kinds["userMessage"] {
		live = completed[1:]
	}
	require.Len(t, turns[0].Items, len(live)+1)
	for index, item := range live {
		require.JSONEq(t, string(item), string(turns[0].Items[index+1]), "持久历史必须与实时条目一致")
	}

	failedID, events := run("EVENTS001_FAIL")
	var terminal struct {
		Turn struct {
			ID, Status string
			Error      *struct{ Message string }
		}
	}
	require.NoError(t, json.Unmarshal(events[len(events)-1].Params, &terminal))
	require.Equal(t, failedID, terminal.Turn.ID)
	require.Equal(t, "failed", terminal.Turn.Status, "模型故障必须以失败终态结束")
	require.NotNil(t, terminal.Turn.Error, "失败终态必须携带错误")
	turns = history()
	require.Len(t, turns, 2)
	require.Equal(t, failedID, turns[1].ID)
	require.Equal(t, "failed", turns[1].Status, "持久历史必须保留失败终态")
	for _, raw := range turns[1].Items {
		var item struct{ Status string }
		require.NoError(t, json.Unmarshal(raw, &item))
		require.NotEqual(t, "inProgress", item.Status, "失败回合不能残留进行中的条目")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	require.Equal(t, map[string]int{"EVENTS001_FULL": 3, "EVENTS001_FAIL": 1}, fixture.steps, "模型故障不能自动重放")
}
