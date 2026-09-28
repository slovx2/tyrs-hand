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

func TestRuntimeCodexTurnSettingsRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-turn-settings")
}

type runtimeCodexTurnSettingsFixture struct {
	root               string
	gates              [2]*runtimeTurnGate
	calls, activeCalls atomic.Int64
}

func newRuntimeCodexTurnSettingsFixture(root string) *runtimeCodexTurnSettingsFixture {
	return &runtimeCodexTurnSettingsFixture{root: root, gates: [2]*runtimeTurnGate{newRuntimeTurnGate(), newRuntimeTurnGate()}}
}

func (f *runtimeCodexTurnSettingsFixture) unblock() {
	for _, gate := range f.gates {
		gate.unblock()
	}
}

func (f *runtimeCodexTurnSettingsFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	t.Helper()
	f.calls.Add(1)
	require.Equal(t, "/v1/responses", request.URL.Path)
	var input struct {
		Model       string
		Reasoning   struct{ Effort, Summary string }
		ServiceTier *string `json:"service_tier"`
		Input       json.RawMessage
	}
	require.NoError(t, json.Unmarshal(body, &input))
	text := string(input.Input)
	step := int64(0)
	if !strings.Contains(text, "TURNSETTINGS_OTHER") && !strings.Contains(text, "TURNSETTINGS_FUTURE") && !strings.Contains(text, "TURNSETTINGS_RESTART") {
		require.Contains(t, text, "TURNSETTINGS_ACTIVE")
		step = f.activeCalls.Add(1)
		require.LessOrEqual(t, step, int64(3), "同一回合仅允许两次工具和一次最终回复")
	}
	t.Logf("TURNSETTINGS-003 step=%d model=%s effort=%s summary=%s tier=%v", step, input.Model, input.Reasoning.Effort, input.Reasoning.Summary, input.ServiceTier)
	if step >= 2 {
		require.Equal(t, "gpt-6-sol", input.Model)
		if step == 2 {
			require.Equal(t, "high", input.Reasoning.Effort)
			require.Equal(t, "detailed", input.Reasoning.Summary)
			require.NotNil(t, input.ServiceTier)
			require.Equal(t, "priority", *input.ServiceTier)
		} else {
			require.Equal(t, "medium", input.Reasoning.Effort)
			require.Equal(t, "auto", input.Reasoning.Summary)
			require.Nil(t, input.ServiceTier, "显式 null 必须清除活动回合 tier")
		}
		var outputs []struct {
			Type   string
			CallID string `json:"call_id"`
			Output json.RawMessage
		}
		require.NoError(t, json.Unmarshal(input.Input, &outputs))
		found := false
		for _, output := range outputs {
			if output.Type == "custom_tool_call_output" && output.CallID == fmt.Sprintf("settings-tool-%d", step-1) {
				require.Contains(t, string(output.Output), fmt.Sprintf("SETTINGS_TOOL_%d", step-1))
				found = true
			}
		}
		require.True(t, found, "实际工具输出必须进入下一次模型请求，不能用调用参数代替结果")
	} else {
		require.Equal(t, "gpt-6-astra", input.Model)
		require.Equal(t, "low", input.Reasoning.Effort)
		require.Equal(t, "auto", input.Reasoning.Summary)
		require.Nil(t, input.ServiceTier, "其他会话、未来回合和重启不能继承活动覆盖")
	}
	if step == 0 || step == 3 {
		runtimeTextModel(w, request, "TURNSETTINGS_DONE", fmt.Sprintf("settings-%d", f.calls.Load()))
		return
	}
	gate := f.gates[step-1]
	close(gate.entered)
	select {
	case <-gate.release:
	case <-request.Context().Done():
		return
	}
	var items []struct {
		Type  string
		Tools []struct {
			Name  string
			Tools []struct{ Name, Description string }
		}
	}
	require.NoError(t, json.Unmarshal(input.Input, &items))
	declared := false
	for _, item := range items {
		if item.Type != "additional_tools" {
			continue
		}
		for _, namespace := range item.Tools {
			if namespace.Name != "functions" {
				continue
			}
			for _, tool := range namespace.Tools {
				declared = declared || tool.Name == "exec" && strings.Contains(tool.Description, "exec_command")
			}
		}
	}
	require.True(t, declared, "只能调用官方 CLI 实际声明的 functions.exec 与嵌套 exec_command")
	args, err := json.Marshal(map[string]any{"cmd": fmt.Sprintf("printf '%d\\n' >> settings-effects.txt; printf 'SETTINGS_TOOL_%d\\n'", step, step), "workdir": filepath.Join(f.root, "project"), "login": false})
	require.NoError(t, err)
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range []struct {
		kind  string
		value map[string]any
	}{
		{"response.created", map[string]any{"response": map[string]any{"id": fmt.Sprintf("settings-tool-%d", step)}}},
		{"response.output_item.done", map[string]any{"item": map[string]any{"type": "custom_tool_call", "name": "exec", "namespace": "functions", "call_id": fmt.Sprintf("settings-tool-%d", step), "input": "text(await tools.exec_command(" + string(args) + "))"}}},
		{"response.completed", map[string]any{"response": map[string]any{"id": fmt.Sprintf("settings-tool-%d", step)}}},
	} {
		event.value["type"] = event.kind
		data, err := json.Marshal(event.value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.kind, data)
		require.NoError(t, err)
	}
}

// TURNSETTINGS-003：实际模型请求和工具副作用验证活动覆盖，管理读取不能代替生效证据。
func verifyRuntimeCodexTurnSettings(t *testing.T, ctx context.Context, registry *RuntimeRegistry,
	connection *ssh.Client, fixture *runtimeCodexTurnSettingsFixture, root string,
) {
	t.Helper()
	client, trace := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{})
	var catalog struct {
		Data []struct {
			Model  string
			Hidden bool
		}
	}
	require.NoError(t, client.Call(ctx, "model/list", map[string]any{}, &catalog))
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-5.5"} {
		found := false
		for _, entry := range catalog.Data {
			found = found || entry.Model == model && !entry.Hidden
		}
		require.True(t, found, "活动切换必须使用锁定CLI的真实模型目录: %s", model)
	}
	newThread := func() string {
		return readSessionThread(t, ctx, client, "thread/start", map[string]any{
			"cwd": filepath.Join(root, "project"), "model": "gpt-6-astra", "approvalPolicy": "never", "sandbox": "danger-full-access",
		}).ID
	}
	thread, other := newThread(), newThread()
	events := client.Subscribe(codex.ThreadFilter{ThreadID: thread})
	defer events.Close()
	var started struct{ Turn struct{ ID string } }
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread, "input": []map[string]string{{"type": "text", "text": "TURNSETTINGS_ACTIVE"}}}, &started))
	awaitGate := func(index int) {
		select {
		case <-fixture.gates[index].entered:
		case <-ctx.Done():
			t.Fatal("真实模型请求未到达设置更新边界")
		}
	}
	update := func(params map[string]any, status string) {
		var response struct{ Status string }
		require.NoError(t, client.Call(ctx, "turn/settings/update", params, &response))
		require.Equal(t, status, response.Status)
	}
	awaitGate(0)
	update(map[string]any{"threadId": other, "turnId": started.Turn.ID, "model": "gpt-6-sol"}, "targetUnavailable")
	update(map[string]any{"threadId": thread, "turnId": "not-the-active-turn", "effort": "high"}, "targetUnavailable")
	trace.expectRequestError("turn/settings/update", -32600)
	err := client.Call(ctx, "turn/settings/update", map[string]any{"threadId": thread, "turnId": started.Turn.ID,
		"model": "gpt-5.5", "effort": "high"}, nil)
	require.ErrorContains(t, err, "the destination changes the admitted node REPL review requirement")
	update(map[string]any{"threadId": thread, "turnId": started.Turn.ID, "model": "gpt-6-sol", "effort": "high", "summary": "detailed", "serviceTier": "fast", "approvalsReviewer": "user"}, "applied")
	runNativeMetadataTurn(t, ctx, client, other, "TURNSETTINGS_OTHER")
	fixture.gates[0].unblock()
	awaitGate(1)
	update(map[string]any{"threadId": thread, "turnId": started.Turn.ID, "model": nil, "effort": "medium", "summary": "auto", "serviceTier": nil}, "applied")
	fixture.gates[1].unblock()
	for done := false; !done; {
		select {
		case <-ctx.Done():
			t.Fatal("活动设置回合未收到终态")
		case event, ok := <-events.Events():
			require.True(t, ok)
			if event.Method != "turn/completed" {
				continue
			}
			var params struct {
				ThreadID string
				Turn     struct {
					ID, Status string
					Error      any
				}
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			require.Equal(t, thread, params.ThreadID)
			require.Equal(t, started.Turn.ID, params.Turn.ID)
			require.Equal(t, "completed", params.Turn.Status)
			require.Nil(t, params.Turn.Error)
			done = true
		}
	}
	update(map[string]any{"threadId": thread, "turnId": started.Turn.ID, "effort": "high"}, "targetUnavailable")
	runNativeMetadataTurn(t, ctx, client, thread, "TURNSETTINGS_FUTURE")
	trace.expectClose("runtime-restart")
	otherGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client, _ = connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{})
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread})
	require.Equal(t, int64(5), fixture.calls.Load(), "重启和恢复不得调用模型")
	runNativeMetadataTurn(t, ctx, client, thread, "TURNSETTINGS_RESTART")
	require.Equal(t, int64(6), fixture.calls.Load())
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
	contents, err := os.ReadFile(filepath.Join(root, "project", "settings-effects.txt"))
	require.NoError(t, err)
	require.Equal(t, "1\n2\n", string(contents), "设置更新、未来回合和重启不能重放工具")
}
