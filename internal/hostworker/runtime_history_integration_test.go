//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

type runtimeHistoryFixture struct {
	t         *testing.T
	root      string
	modelStep atomic.Int64
	toolCalls atomic.Int64
	records   []runtimeHistoryRecord
}

type runtimeHistoryRecord struct {
	id, mode string
	turns    []string
}
type runtimeHistoryTurn struct {
	ID        string           `json:"id"`
	Items     []map[string]any `json:"items"`
	ItemsView string           `json:"itemsView"`
}
type runtimeHistoryItem struct {
	TurnID string         `json:"turnId"`
	Item   map[string]any `json:"item"`
}

func historyLargeOutput() string {
	return "OUTPUT_BEGIN\n" + strings.Repeat("完整工具历史0123456789\n", 4096) + "OUTPUT_END"
}

func (f *runtimeHistoryFixture) tool(_ context.Context, request codex.ServerRequest) (any, error) {
	if request.Method != "item/tool/call" {
		return nil, fmt.Errorf("未预期的回调 %s", request.Method)
	}
	var params struct {
		Tool      string `json:"tool"`
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return nil, err
	}
	if params.Tool != "history_output" || params.Namespace != "history" {
		return nil, fmt.Errorf("动态工具路由标识错误")
	}
	f.toolCalls.Add(1)
	if err := os.WriteFile(filepath.Join(f.root, "history-result.txt"), []byte(historyLargeOutput()), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "contentItems": []map[string]any{{"type": "inputText", "text": historyLargeOutput()}}}, nil
}

func (f *runtimeHistoryFixture) model(w http.ResponseWriter, body []byte) {
	step := f.modelStep.Add(1) - 1
	var request struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		f.t.Error(err)
		http.Error(w, "bad request", 400)
		return
	}
	if step >= 8 {
		f.t.Error("历史读取错误触发模型")
		http.Error(w, "unexpected model call", 400)
		return
	}
	if step%4 == 1 && !strings.Contains(string(request.Messages), "tool_result") {
		f.t.Error("SDK 未收到动态工具结果")
	}
	block := map[string]any{"type": "text", "text": ""}
	delta := map[string]any{"type": "text_delta", "text": fmt.Sprintf("HISTORY_ANSWER_%d", step)}
	stop := "end_turn"
	if step%4 == 0 {
		name := ""
		for _, tool := range request.Tools {
			if strings.HasPrefix(tool.Name, "mcp__tyrs_hand__") {
				name = tool.Name
				break
			}
		}
		if name == "" {
			f.t.Error("动态工具没有进入真实模型请求")
			http.Error(w, "missing dynamic tool", 400)
			return
		}
		block = map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_history_%d", step), "name": name, "input": map[string]any{}}
		delta = map[string]any{"type": "input_json_delta", "partial_json": "{}"}
		stop = "tool_use"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, _ := json.Marshal(value)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
	}
	event("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("msg_history_%d", step), "type": "message", "role": "assistant", "model": "claude-config-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	event("content_block_start", map[string]any{"index": 0, "content_block": block})
	event("content_block_delta", map[string]any{"index": 0, "delta": delta})
	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 5}})
	event("message_stop", map[string]any{})
}

func (f *runtimeHistoryFixture) create(ctx context.Context, client *codex.SocketClient) {
	t := f.t
	for _, mode := range []string{"legacy", "paginated"} {
		var started struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
			"cwd": f.root, "historyMode": mode, "approvalPolicy": "never", "sandbox": "danger-full-access",
			"dynamicTools": []map[string]any{{"type": "namespace", "name": "history", "description": "历史测试工具", "tools": []map[string]any{
				{"type": "function", "name": "history_output", "description": "写入历史测试文件", "inputSchema": map[string]any{"type": "object", "additionalProperties": false}},
			}}},
		}, &started))
		record := runtimeHistoryRecord{id: started.Thread.ID, mode: mode}
		events := client.Subscribe(codex.ThreadFilter{ThreadID: record.id})
		defer events.Close()
		completedTools := 0
		for index := 0; index < 3; index++ {
			var result struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": record.id,
				"clientUserMessageId": fmt.Sprintf("history-%s-%d", mode, index),
				"input":               []map[string]any{{"type": "text", "text": fmt.Sprintf("HISTORY_INPUT_%s_%d", mode, index)}},
			}, &result))
			record.turns = append(record.turns, result.Turn.ID)
			for done := false; !done; {
				select {
				case <-ctx.Done():
					t.Fatal("历史测试 Turn 超时")
				case event := <-events.Events():
					var payload struct {
						Turn struct{ ID, Status string } `json:"turn"`
						Item map[string]any              `json:"item"`
					}
					require.NoError(t, json.Unmarshal(event.Params, &payload))
					if event.Method == "item/completed" && payload.Item["type"] == "dynamicToolCall" {
						completedTools++
						require.Equal(t, "history_output", payload.Item["tool"])
						require.Equal(t, "history", payload.Item["namespace"])
						require.Equal(t, []any{map[string]any{"type": "inputText", "text": historyLargeOutput()}}, payload.Item["contentItems"])
					}
					if event.Method == "turn/completed" {
						require.Equal(t, result.Turn.ID, payload.Turn.ID)
						require.Equal(t, "completed", payload.Turn.Status)
						done = true
					}
				}
			}
		}
		require.Equal(t, 1, completedTools, "原始动态工具完成事件不能重复或丢失")
		f.records = append(f.records, record)
	}
	require.Equal(t, int64(2), f.toolCalls.Load())
	content, err := os.ReadFile(filepath.Join(f.root, "history-result.txt"))
	require.NoError(t, err)
	require.Equal(t, historyLargeOutput(), string(content))
}

func historyPages[T any](t *testing.T, ctx context.Context, client *codex.SocketClient, method string, params map[string]any) []T {
	t.Helper()
	var all []T
	for page := 0; page < 30; page++ {
		var result struct {
			Data       []T     `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		params["limit"] = 2
		require.NoError(t, client.Call(ctx, method, params, &result))
		all = append(all, result.Data...)
		if result.NextCursor == nil {
			return all
		}
		params["cursor"] = result.NextCursor
	}
	t.Fatal("历史分页不能收敛")
	return nil
}

func (f *runtimeHistoryFixture) verify(ctx context.Context, client *codex.SocketClient) {
	t := f.t
	for _, record := range f.records {
		var response struct {
			Thread struct {
				HistoryMode string               `json:"historyMode"`
				Turns       []runtimeHistoryTurn `json:"turns"`
			} `json:"thread"`
		}
		require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": record.id, "includeTurns": false}, &response))
		require.Empty(t, response.Thread.Turns)
		require.Equal(t, record.mode, response.Thread.HistoryMode)
		require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": record.id, "includeTurns": true}, &response))
		require.Len(t, response.Thread.Turns, 3)
		var expectedItems []runtimeHistoryItem
		for index, turn := range response.Thread.Turns {
			require.Equal(t, record.turns[index], turn.ID)
			for _, item := range turn.Items {
				expectedItems = append(expectedItems, runtimeHistoryItem{TurnID: turn.ID, Item: item})
			}
		}
		for _, view := range []string{"full", "summary", "notLoaded"} {
			for _, direction := range []string{"asc", "desc"} {
				listed := historyPages[runtimeHistoryTurn](t, ctx, client, "thread/turns/list", map[string]any{"threadId": record.id, "itemsView": view, "sortDirection": direction})
				expected := slices.Clone(response.Thread.Turns)
				if direction == "desc" {
					slices.Reverse(expected)
				}
				require.Len(t, listed, len(expected))
				for index, turn := range listed {
					require.Equal(t, expected[index].ID, turn.ID)
					require.Equal(t, view, turn.ItemsView)
					switch view {
					case "full":
						require.Equal(t, expected[index].Items, turn.Items)
					case "notLoaded":
						require.Empty(t, turn.Items)
					case "summary":
						require.Len(t, turn.Items, 2)
						require.Equal(t, "userMessage", turn.Items[0]["type"])
						require.Equal(t, "agentMessage", turn.Items[1]["type"])
					}
				}
			}
		}
		items := historyPages[runtimeHistoryItem](t, ctx, client, "thread/items/list", map[string]any{"threadId": record.id, "sortDirection": "asc"})
		require.Equal(t, expectedItems, items)
		found := 0
		for _, entry := range items {
			if entry.Item["type"] == "dynamicToolCall" {
				found++
				require.Equal(t, []any{map[string]any{"type": "inputText", "text": historyLargeOutput()}}, entry.Item["contentItems"])
			}
		}
		require.Equal(t, 1, found)
		slices.Reverse(expectedItems)
		require.Equal(t, expectedItems, historyPages[runtimeHistoryItem](t, ctx, client, "thread/items/list", map[string]any{"threadId": record.id, "sortDirection": "desc"}))
	}
}
