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
	"github.com/stretchr/testify/require"
)

func TestRuntimeCodexEventsRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-events")
}

type runtimeCodexEventsFixture struct {
	root  string
	calls atomic.Int64
}

const nativeEventText = "EVENT_TEXT_A_B"
const nativeEventPlan = "# 原生计划\n- 检查事件顺序\n"

func (f *runtimeCodexEventsFixture) model(t *testing.T, w http.ResponseWriter, body []byte) {
	step := f.calls.Add(1)
	require.LessOrEqual(t, step, int64(6), "不能重放或额外发起模型请求")
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
		require.NoError(t, err)
		w.(http.Flusher).Flush()
	}
	id := fmt.Sprintf("native-events-%d", step)
	event("response.created", map[string]any{"response": map[string]any{"id": id}})
	if step == 1 {
		event("response.output_item.added", map[string]any{"item": map[string]any{"type": "reasoning", "id": "native-reasoning", "summary": []any{}}})
		event("response.reasoning_summary_part.added", map[string]any{"summary_index": 0})
		for _, delta := range []string{"SUMMARY_A", "_B"} {
			event("response.reasoning_summary_text.delta", map[string]any{"summary_index": 0, "delta": delta})
		}
		for _, delta := range []string{"BODY_A", "_B"} {
			event("response.reasoning_text.delta", map[string]any{"content_index": 0, "delta": delta})
		}
		event("response.output_item.done", map[string]any{"item": map[string]any{"type": "reasoning", "id": "native-reasoning",
			"summary": []map[string]string{{"type": "summary_text", "text": "SUMMARY_A_B"}},
			"content": []map[string]string{{"type": "reasoning_text", "text": "BODY_A_B"}}}})
	}
	if step == 3 || step == 5 {
		var request struct{ Tools []struct{ Name string } }
		require.NoError(t, json.Unmarshal(body, &request))
		available := false
		for _, tool := range request.Tools {
			available = available || tool.Name == "shell_command"
		}
		require.True(t, available, "必须使用固定 CLI 实际声明的 shell_command")
		command := "printf 'CMD_A\\n'; sleep 0.1; printf 'CMD_B\\n'; printf 'EVENT_EFFECT\\n' > event-effect.txt"
		if step == 5 {
			// Codex 原生识别 shell 中的 apply_patch，产生真实文件补丁事件。
			command = "apply_patch <<'PATCH'\n*** Begin Patch\n*** Add File: event-patch.txt\n+PATCH_EFFECT\n*** End Patch\nPATCH"
		}
		args, err := json.Marshal(map[string]any{"command": command, "workdir": filepath.Join(f.root, "project")})
		require.NoError(t, err)
		event("response.output_item.done", map[string]any{"item": map[string]any{"type": "function_call", "name": "shell_command",
			"call_id": "native-event-tool", "arguments": string(args)}})
	} else {
		if step == 4 || step == 6 {
			f.verifyToolResult(t, body, step)
		}
		text := nativeEventText
		chunks := []string{"EVENT_TEXT_A", "_B"}
		if step == 2 {
			text = "<proposed_plan>\n" + nativeEventPlan + "</proposed_plan>"
			chunks = []string{"<proposed_", "plan>\n# 原生计划\n", "- 检查事件顺序\n", "</proposed_plan>"}
		}
		event("response.output_item.added", map[string]any{"item": map[string]any{"type": "message", "id": id + "-message", "role": "assistant", "content": []any{}}})
		for _, chunk := range chunks {
			event("response.output_text.delta", map[string]any{"delta": chunk})
		}
		event("response.output_item.done", map[string]any{"item": map[string]any{"type": "message", "id": id + "-message", "role": "assistant",
			"content": []map[string]string{{"type": "output_text", "text": text}}}})
	}
	event("response.completed", map[string]any{"response": map[string]any{"id": id,
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}})
}

func (f *runtimeCodexEventsFixture) verifyToolResult(t *testing.T, body []byte, step int64) {
	t.Helper()
	var request struct {
		Input []struct {
			Type   string
			CallID string `json:"call_id"`
			Output string
		}
	}
	require.NoError(t, json.Unmarshal(body, &request))
	found := false
	for _, item := range request.Input {
		if item.Type != "function_call_output" || item.CallID != "native-event-tool" {
			continue
		}
		found = true
		if step == 4 {
			require.Contains(t, item.Output, "CMD_A\nCMD_B\n")
		} else {
			require.Contains(t, item.Output, "Success. Updated the following files:")
			require.Contains(t, item.Output, "event-patch.txt")
		}
	}
	require.True(t, found, "真实工具结果必须进入模型续写上下文")
	name, content := "event-effect.txt", "EVENT_EFFECT\n"
	if step == 6 {
		name, content = "event-patch.txt", "PATCH_EFFECT\n"
	}
	actual, err := os.ReadFile(filepath.Join(f.root, "project", name))
	require.NoError(t, err)
	require.Equal(t, content, string(actual))
}

type nativeEventItem struct {
	ID, Type, Text, Status, AggregatedOutput string
	Summary                                  []string
	Content                                  []any
	ExitCode                                 *int
	Changes                                  []struct {
		Path, Diff string
		Kind       struct{ Type string }
	}
}

// EVENTS-008：只声明真实 CLI 实际产生的流、顺序、历史和工具副作用。
func verifyCodexNativeEvents(t *testing.T, ctx context.Context, client *codex.SocketClient, fixture *runtimeCodexEventsFixture) {
	t.Helper()
	for _, scenario := range []string{"text", "plan", "command", "patch"} {
		if !t.Run(scenario, func(t *testing.T) {
			thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
				"cwd": filepath.Join(fixture.root, "project"), "approvalPolicy": "never", "sandbox": "danger-full-access",
				"historyMode": "paginated",
				// shell_command 的原生执行器发出输出 delta；unified_exec 的短命令仅发终态。
				"config": map[string]any{"show_raw_agent_reasoning": true, "features.unified_exec": false}})
			events := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
			defer events.Close()
			params := map[string]any{"threadId": thread.ID,
				"input": []map[string]any{{"type": "text", "text": "EVENTS_NATIVE_" + scenario, "text_elements": []any{}}}}
			if scenario == "plan" {
				params["collaborationMode"] = map[string]any{"mode": "plan", "settings": map[string]any{
					"model": "mock-model", "reasoning_effort": "medium", "developer_instructions": nil}}
			}
			var started struct{ Turn struct{ ID string } }
			require.NoError(t, client.Call(ctx, "turn/start", params, &started))
			verifyNativeEventTurn(t, ctx, client, events, thread.ID, started.Turn.ID, scenario, fixture.root)
		}) {
			return
		}
	}
	require.Equal(t, int64(6), fixture.calls.Load())
}

func verifyNativeEventTurn(t *testing.T, ctx context.Context, client *codex.SocketClient,
	events *codex.EventSubscription, threadID, turnID, scenario, root string,
) {
	t.Helper()
	started, completed := false, false
	starts, ends := map[string]string{}, map[string]nativeEventItem{}
	completedItems := []nativeEventItem{}
	deltas := map[string]string{}
	sequence := []string{}
	diffs := []string{}
	summaryParts := 0
	deltaTypes := map[string]string{"item/agentMessage/delta": "agentMessage", "item/plan/delta": "plan",
		"item/reasoning/summaryTextDelta": "reasoning", "item/reasoning/textDelta": "reasoning",
		"item/commandExecution/outputDelta": "commandExecution"}
	for !completed {
		select {
		case <-ctx.Done():
			t.Fatal("真实 Codex 流式事件超时")
		case event, ok := <-events.Events():
			require.True(t, ok)
			var params struct {
				ThreadID, TurnID, ItemID, Delta, Diff string
				SummaryIndex, ContentIndex            int
				Item                                  nativeEventItem
				Thread                                struct{ ID string }
				Turn                                  struct {
					ID, Status string
					Error      any
				}
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			// thread/start 的响应与 thread/started 通知异步到达，通知可能在订阅
			// 建立之后才被读取。固定协议将此方法的会话身份放在 thread.id，
			// 它不是全局事件；真正无会话的账号通知已由 ThreadFilter 排除。
			if event.Method == "thread/started" {
				require.Equal(t, threadID, params.Thread.ID, "thread/started 必须属于当前会话")
				sequence = append(sequence, event.Method)
				continue
			}
			require.Equal(t, threadID, params.ThreadID, "%s 必须携带当前会话的顶层 threadId", event.Method)
			if params.TurnID != "" {
				require.Equal(t, turnID, params.TurnID)
			}
			sequence = append(sequence, event.Method)
			switch event.Method {
			case "turn/started":
				require.False(t, started, "Turn 开始事件必须唯一")
				require.Equal(t, turnID, params.Turn.ID)
				started = true
			case "item/started":
				require.True(t, started)
				require.NotContains(t, starts, params.Item.ID)
				starts[params.Item.ID] = params.Item.Type
			case "item/completed":
				require.Equal(t, params.Item.Type, starts[params.Item.ID], "必须先收到同一 Item 的开始事件")
				require.NotContains(t, ends, params.Item.ID)
				ends[params.Item.ID] = params.Item
				completedItems = append(completedItems, params.Item)
			case "item/agentMessage/delta", "item/plan/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta", "item/commandExecution/outputDelta":
				require.Equal(t, deltaTypes[event.Method], starts[params.ItemID], "流式事件必须位于同类型 Item 开始之后")
				require.NotContains(t, ends, params.ItemID, "流式事件必须位于 Item 终态之前")
				if event.Method == "item/reasoning/summaryTextDelta" {
					require.Equal(t, 1, summaryParts, "摘要分片必须在 summaryPartAdded 之后")
				}
				require.Zero(t, params.SummaryIndex)
				require.Zero(t, params.ContentIndex)
				deltas[event.Method] += params.Delta
			case "item/reasoning/summaryPartAdded":
				require.Equal(t, "reasoning", starts[params.ItemID])
				require.NotContains(t, ends, params.ItemID)
				require.Zero(t, params.SummaryIndex)
				summaryParts++
			case "turn/diff/updated":
				require.Equal(t, "patch", scenario)
				require.Equal(t, "completed", ends["native-event-tool"].Status, "Diff 只能来自实际完成的补丁")
				diffs = append(diffs, params.Diff)
			case "turn/completed":
				require.True(t, started)
				require.Equal(t, turnID, params.Turn.ID)
				require.Equal(t, "completed", params.Turn.Status)
				require.Nil(t, params.Turn.Error)
				completed = true
			}
		}
	}
	require.Len(t, ends, len(starts), "全部 Item 都必须闭合")
	wanted := map[string]string{"item/agentMessage/delta": nativeEventText}
	switch scenario {
	case "text":
		wanted["item/reasoning/summaryTextDelta"] = "SUMMARY_A_B"
		wanted["item/reasoning/textDelta"] = "BODY_A_B"
		require.Equal(t, 1, summaryParts)
		require.Equal(t, []string{"SUMMARY_A_B"}, ends["native-reasoning"].Summary)
		require.Equal(t, []any{"BODY_A_B"}, ends["native-reasoning"].Content)
	case "plan":
		wanted = map[string]string{"item/plan/delta": nativeEventPlan}
	case "command":
		wanted["item/commandExecution/outputDelta"] = "CMD_A\nCMD_B\n"
		item := ends["native-event-tool"]
		require.Equal(t, "commandExecution", item.Type)
		require.Equal(t, "completed", item.Status)
		require.NotNil(t, item.ExitCode)
		require.Zero(t, *item.ExitCode)
		require.Equal(t, wanted["item/commandExecution/outputDelta"], item.AggregatedOutput)
	case "patch":
		item := ends["native-event-tool"]
		require.Equal(t, "fileChange", item.Type)
		require.Equal(t, "completed", item.Status)
		require.Len(t, item.Changes, 1)
		require.Equal(t, filepath.Join(root, "project", "event-patch.txt"), item.Changes[0].Path)
		require.Equal(t, "add", item.Changes[0].Kind.Type)
		require.Equal(t, "PATCH_EFFECT\n", item.Changes[0].Diff)
		require.NotEmpty(t, diffs)
		require.Contains(t, diffs[len(diffs)-1], "+PATCH_EFFECT")
	}
	require.Equal(t, wanted, deltas, "流分片拼接结果必须精确且不能跨事件类型")
	for _, item := range ends {
		if item.Type == "agentMessage" {
			require.Equal(t, nativeEventText, item.Text)
		}
		if item.Type == "plan" {
			require.Equal(t, nativeEventPlan, item.Text)
		}
	}
	effects := map[string]string{}
	for name, content := range map[string]string{"command": "EVENT_EFFECT\n", "patch": "PATCH_EFFECT\n"} {
		if scenario == name {
			file := "event-effect.txt"
			if name == "patch" {
				file = "event-patch.txt"
			}
			actual, err := os.ReadFile(filepath.Join(root, "project", file))
			require.NoError(t, err)
			require.Equal(t, content, string(actual))
			effects[file] = string(actual)
		}
	}
	var history struct {
		Thread struct {
			Turns []struct {
				ID, Status string
				Items      []nativeEventItem
			}
		}
	}
	require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &history))
	require.Len(t, history.Thread.Turns, 1)
	require.Equal(t, turnID, history.Thread.Turns[0].ID)
	require.Equal(t, "completed", history.Thread.Turns[0].Status)
	require.Equal(t, completedItems, history.Thread.Turns[0].Items,
		"原生 paginated 历史必须完整保留实时 Item 的 ID、内容和顺序")
	// thread/read 响应形成同连接屏障；此前排队的重复终态或迟到流不能被忽略。
	for {
		select {
		case event, ok := <-events.Events():
			require.True(t, ok, "校验屏障前连接不能关闭")
			require.NotEqual(t, "turn/completed", event.Method)
			require.False(t, strings.HasPrefix(event.Method, "item/"), "成功终态后不能继续发 Item 事件")
		default:
			if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
				data, err := json.MarshalIndent(map[string]any{"formatVersion": 1, "runId": os.Getenv("PROTOCOL_RUN_ID"),
					"engine": "codex", "caseName": "TestRuntimeCodexEventsRealSSH", "caseIds": []string{"EVENTS-008"}, "kind": "event-effects",
					"payload": map[string]any{"scenario": scenario, "threadId": threadID, "turnId": turnID,
						"sequence": sequence, "deltas": deltas, "items": ends, "effects": effects, "historyVerified": true}}, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(directory, "codex-event-effects-"+scenario+".json"), data, 0o600))
			}
			return
		}
	}
}
