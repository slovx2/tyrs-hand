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
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeIsolationRealSSHBothEngines(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "isolation")
}

type runtimeIsolationFixture struct {
	root, secret string
	calls, tools map[runtimeidentity.Engine]*atomic.Int64
	prompts      chan runtimeApprovalPrompt
}

func newRuntimeIsolationFixture(root, secret string) *runtimeIsolationFixture {
	f := &runtimeIsolationFixture{root: root, secret: secret, prompts: make(chan runtimeApprovalPrompt, 4), calls: map[runtimeidentity.Engine]*atomic.Int64{}, tools: map[runtimeidentity.Engine]*atomic.Int64{}}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		f.calls[engine], f.tools[engine] = &atomic.Int64{}, &atomic.Int64{}
	}
	return f
}

func (f *runtimeIsolationFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, engine runtimeidentity.Engine, body []byte) {
	step := f.calls[engine].Add(1)
	var input struct {
		Model string
		Tools []struct{ Name string }
	}
	require.NoError(t, json.Unmarshal(body, &input))
	other := runtimeidentity.Claude
	if engine == runtimeidentity.Claude {
		other = runtimeidentity.Codex
		require.Equal(t, "claude-config-model", input.Model)
		require.Equal(t, "test-not-a-secret", request.Header.Get("x-api-key"))
		require.Empty(t, request.Header.Get("Authorization"))
	} else {
		require.Equal(t, "mock-model", input.Model)
		require.Equal(t, "Bearer isolation-codex-key", request.Header.Get("Authorization"))
		require.Empty(t, request.Header.Get("x-api-key"))
	}
	require.False(t, strings.Contains(string(body), "ISOLATED_ANSWER_"+string(other)), "另一引擎的历史不得进入模型")
	require.False(t, strings.Contains(string(body), f.secret+"-"+string(other)), "相同工具 ID 不能复用另一引擎的缓存结果")
	answer := ""
	if step == 2 {
		require.True(t, strings.Contains(string(body), f.secret+"-"+string(engine)), "真实文件工具结果必须回到所属模型")
		answer = "ISOLATED_ANSWER_" + string(engine)
		if engine == runtimeidentity.Codex {
			runtimeTextModel(w, request, answer, "isolation-"+string(engine))
			return
		}
	}
	if engine == runtimeidentity.Claude && step == 4 {
		requireGoalToolResult(t, body, "native_isolation_write", "approval-isolated.txt")
		answer = "ISOLATED_APPROVAL_DONE"
	}
	if answer == "" && step != 1 && (engine != runtimeidentity.Claude || step != 3) {
		t.Error("隔离读取、重试或错误入口回答触发了额外模型请求")
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
	if engine == runtimeidentity.Codex {
		event("response.created", map[string]any{"response": map[string]any{"id": "isolation-codex"}})
		event("response.output_item.done", map[string]any{"item": map[string]any{"type": "function_call", "call_id": "same-tool-call-id", "namespace": "isolation", "name": "effect", "arguments": "{}"}})
		event("response.completed", map[string]any{"response": map[string]any{"id": "isolation-codex", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}})
		return
	}
	name, id, arguments := "", "same-tool-call-id", "{}"
	for _, tool := range input.Tools {
		if strings.HasPrefix(tool.Name, "mcp__tyrs_hand__") {
			name = tool.Name
			break
		}
	}
	if step == 3 {
		name, id = "Write", "native_isolation_write"
		value, err := json.Marshal(map[string]any{"file_path": filepath.Join(f.root, "approval-isolated.txt"), "content": f.secret + "-approved"})
		require.NoError(t, err)
		arguments = string(value)
	}
	require.NotEmpty(t, name)
	block := map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}
	delta := map[string]any{"type": "input_json_delta", "partial_json": arguments}
	stop := "tool_use"
	if answer != "" {
		block = map[string]any{"type": "text", "text": ""}
		delta = map[string]any{"type": "text_delta", "text": answer}
		stop = "end_turn"
	}
	event("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("msg_isolation_%d", step), "type": "message", "role": "assistant", "model": "claude-config-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	event("content_block_start", map[string]any{"index": 0, "content_block": block})
	event("content_block_delta", map[string]any{"index": 0, "delta": delta})
	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 5}})
	event("message_stop", map[string]any{})
}

func (f *runtimeIsolationFixture) callback(t *testing.T, engine runtimeidentity.Engine) codex.ServerRequestHandler {
	return func(ctx context.Context, request codex.ServerRequest) (any, error) {
		if request.Method == "item/tool/call" {
			var params struct{ Namespace, Tool, CallID string }
			if err := json.Unmarshal(request.Params, &params); err != nil {
				return nil, err
			}
			require.Equal(t, "isolation", params.Namespace)
			require.Equal(t, "effect", params.Tool)
			require.Equal(t, "same-tool-call-id", params.CallID)
			f.tools[engine].Add(1)
			value := f.secret + "-" + string(engine)
			file, err := os.OpenFile(filepath.Join(f.root, string(engine)+"-effect.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, err
			}
			_, err = file.WriteString(value)
			closeErr := file.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
			return map[string]any{"success": true, "contentItems": []map[string]any{{"type": "inputText", "text": value}}}, nil
		}
		if engine != runtimeidentity.Claude || request.Method != "item/fileChange/requestApproval" {
			return nil, fmt.Errorf("审批或工具错误路由到 %s: %s", engine, request.Method)
		}
		prompt := runtimeApprovalPrompt{request: request, reply: make(chan any, 1)}
		f.prompts <- prompt
		select {
		case answer := <-prompt.reply:
			return answer, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func verifyIsolationConfigs(t *testing.T, ctx context.Context, clients map[runtimeidentity.Engine]*codex.SocketClient) {
	t.Helper()
	files := map[runtimeidentity.Engine]string{}
	type configRead struct {
		Config map[string]any
		Layers []struct{ Name struct{ Type, File string } }
	}
	for engine, client := range clients {
		result := nativeMetadataCall[configRead](t, ctx, client, "config/read", map[string]any{"includeLayers": true})
		for _, layer := range result.Layers {
			if layer.Name.Type == "user" {
				files[engine] = layer.Name.File
			}
		}
		require.NotEmpty(t, files[engine])
	}
	require.NotEqual(t, files[runtimeidentity.Codex], files[runtimeidentity.Claude])
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		other, mode := runtimeidentity.Claude, "read-only"
		if engine == runtimeidentity.Claude {
			other, mode = runtimeidentity.Codex, "workspace-write"
		}
		before, beforeErr := os.ReadFile(files[other])
		require.True(t, beforeErr == nil || os.IsNotExist(beforeErr))
		require.NoError(t, clients[engine].Call(ctx, "config/value/write", map[string]any{"keyPath": "sandbox_mode", "value": mode, "mergeStrategy": "replace"}, nil))
		after, afterErr := os.ReadFile(files[other])
		require.Equal(t, os.IsNotExist(beforeErr), os.IsNotExist(afterErr), "写当前引擎不得创建另一配置文件")
		require.Equal(t, before, after, "配置写入不能修改另一引擎文件")
		result := nativeMetadataCall[configRead](t, ctx, clients[engine], "config/read", map[string]any{})
		require.Equal(t, mode, result.Config["sandbox_mode"])
	}
}

// ISOLATION-004：真实共享项目、同提交和工具 ID；不伪造强制同名 thread ID。
func verifyRuntimeIsolation(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connections map[runtimeidentity.Engine]*ssh.Client, fixture *runtimeIsolationFixture) {
	t.Helper()
	clients := map[runtimeidentity.Engine]*codex.SocketClient{}
	traces := map[runtimeidentity.Engine]*protocolTraceTransport{}
	subscriptions := map[runtimeidentity.Engine]*codex.EventSubscription{}
	threads, turns := map[runtimeidentity.Engine]string{}, map[runtimeidentity.Engine]string{}
	engines := []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude}
	for _, engine := range engines {
		client, trace := connectRuntimeSSHWithTrace(t, ctx, connections[engine], engine, codex.SocketClientOptions{ClientName: "Codex Desktop", ServerRequestHandler: fixture.callback(t, engine)})
		clients[engine], traces[engine] = client, trace
		subscriptions[engine] = client.Subscribe(codex.ThreadFilter{})
		defer subscriptions[engine].Close()
	}
	verifyIsolationConfigs(t, ctx, clients)
	for _, engine := range engines {
		thread := readSessionThread(t, ctx, clients[engine], "thread/start", map[string]any{
			"cwd": fixture.root, "approvalPolicy": "never", "sandbox": "danger-full-access",
			"dynamicTools": []map[string]any{{"type": "namespace", "name": "isolation", "description": "真实隔离副作用", "tools": []map[string]any{{"type": "function", "name": "effect", "description": "追加所属引擎文件", "inputSchema": map[string]any{"type": "object", "additionalProperties": false}}}}},
		})
		threads[engine] = thread.ID
		require.NoError(t, clients[engine].Call(ctx, "thread/name/set", map[string]any{"threadId": thread.ID, "name": "同项目同会话名称"}, nil))
	}
	require.NotEqual(t, threads[runtimeidentity.Codex], threads[runtimeidentity.Claude], "真实原生 ID 分别生成，不强制构造碰撞")
	params := func(engine runtimeidentity.Engine) map[string]any {
		return map[string]any{"threadId": threads[engine], "clientUserMessageId": "same-isolation-submit-id", "input": []map[string]any{{"type": "text", "text": "ISOLATION_SHARED_PROJECT"}}}
	}
	for _, engine := range engines {
		other := runtimeidentity.Claude
		if engine == runtimeidentity.Claude {
			other = runtimeidentity.Codex
		}
		for _, method := range []string{"thread/read", "thread/resume", "thread/settings/update", "turn/start"} {
			foreign := map[string]any{"threadId": threads[other]}
			if method == "thread/settings/update" {
				foreign["approvalPolicy"] = "never"
			}
			if method == "turn/start" {
				foreign["input"] = []map[string]any{{"type": "text", "text": "MUST_NOT_REACH_MODEL"}}
			}
			require.Error(t, clients[engine].Call(ctx, method, foreign, nil), "外部引擎的会话引用必须拒绝: %s", method)
		}
		started := nativeMetadataCall[struct{ Turn struct{ ID string } }](t, ctx, clients[engine], "turn/start", params(engine))
		turns[engine] = started.Turn.ID
		waitSessionTurn(t, ctx, clients[engine], threads[engine], started.Turn.ID)
		data, err := os.ReadFile(filepath.Join(fixture.root, string(engine)+"-effect.txt"))
		require.NoError(t, err)
		require.Equal(t, fixture.secret+"-"+string(engine), string(data), "相同提交和工具 ID 必须分别执行一次实际副作用")
		require.Equal(t, int64(1), fixture.tools[engine].Load())
	}
	// 已有工具副作用不能因提交重试再次执行；此处只声明真实验证的 Claude 幂等行为。
	duplicate := nativeMetadataCall[struct{ Turn struct{ ID string } }](t, ctx, clients[runtimeidentity.Claude], "turn/start", params(runtimeidentity.Claude))
	require.Equal(t, turns[runtimeidentity.Claude], duplicate.Turn.ID)
	approval := nativeMetadataCall[struct{ Turn struct{ ID string } }](t, ctx, clients[runtimeidentity.Claude], "turn/start", map[string]any{
		"threadId": threads[runtimeidentity.Claude], "approvalPolicy": "on-request", "input": []map[string]any{{"type": "text", "text": "ISOLATION_APPROVAL"}},
	})
	var pending runtimeApprovalPrompt
	select {
	case pending = <-fixture.prompts:
	case <-ctx.Done():
		t.Fatal("Claude 原生 Write 未到达审批")
	}
	var prompt struct{ ThreadID, TurnID string }
	require.NoError(t, json.Unmarshal(pending.request.Params, &prompt))
	require.Equal(t, threads[runtimeidentity.Claude], prompt.ThreadID)
	require.Equal(t, approval.Turn.ID, prompt.TurnID)
	sideEffects := func() int {
		contents, err := os.ReadFile(filepath.Join(fixture.root, "approval-isolated.txt"))
		if os.IsNotExist(err) {
			return 0
		}
		require.NoError(t, err)
		require.Equal(t, fixture.secret+"-approved", string(contents))
		return 1
	}
	before := sideEffects()
	require.Zero(t, before)
	wrongAnswer, err := json.Marshal(map[string]any{"id": pending.request.ID, "result": map[string]string{"decision": "accept"}})
	require.NoError(t, err)
	require.NoError(t, traces[runtimeidentity.Codex].WriteMessage(1, wrongAnswer))
	// 注释仅写入测试 trace；应用收到的仍然是上面的原始响应。
	marked := 0
	trace := traces[runtimeidentity.Codex]
	trace.mu.Lock()
	for _, message := range trace.messages {
		id, marshalErr := json.Marshal(message["id"])
		if marshalErr == nil && message["direction"] == "client" && message["method"] == nil && string(id) == string(pending.request.ID) {
			message["expectedForeignServerResponse"] = true
			marked++
		}
	}
	trace.mu.Unlock()
	require.Equal(t, 1, marked, "只允许标注本次真实投递的无匹配响应")
	time.Sleep(200 * time.Millisecond)
	afterForeign := sideEffects()
	require.Zero(t, afterForeign, "向错误引擎发送审批答案不能产生文件副作用")
	require.Equal(t, int64(3), fixture.calls[runtimeidentity.Claude].Load())
	pending.reply <- map[string]string{"decision": "accept"}
	waitSessionTurn(t, ctx, clients[runtimeidentity.Claude], threads[runtimeidentity.Claude], approval.Turn.ID)
	afterAuthorized := sideEffects()
	require.Equal(t, 1, afterAuthorized)
	if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
		artifact, marshalErr := json.MarshalIndent(map[string]any{
			"formatVersion": 1, "runId": os.Getenv("PROTOCOL_RUN_ID"), "caseName": t.Name(),
			"caseIds": []string{"ISOLATION-004"}, "kind": "fault-injection", "payload": map[string]any{
				"type": "foreign-server-response", "sourceEngine": runtimeidentity.Claude, "targetEngine": runtimeidentity.Codex,
				"requestId": pending.request.ID, "requestMethod": pending.request.Method, "reply": json.RawMessage(wrongAnswer),
				"sideEffectsBefore": before, "sideEffectsAfterForeignReply": afterForeign, "sideEffectsAfterAuthorizedReply": afterAuthorized,
			},
		}, "", "  ")
		require.NoError(t, marshalErr)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "fault-injection-isolation.json"), artifact, 0o600))
	}
	for _, engine := range engines {
		other := runtimeidentity.Claude
		if engine == runtimeidentity.Claude {
			other = runtimeidentity.Codex
		}
		starts := 0
	drain:
		for {
			select {
			case event, ok := <-subscriptions[engine].Events():
				require.True(t, ok)
				require.NotContains(t, string(event.Params), threads[other], "无过滤订阅也不能收到另一引擎事件")
				if event.Method == "turn/started" {
					starts++
				}
			default:
				break drain
			}
		}
		expected := 1
		if engine == runtimeidentity.Claude {
			expected = 2
		}
		require.Equal(t, expected, starts)
	}
	traces[runtimeidentity.Claude].expectClose("runtime-restart")
	generation := registry.entries[runtimeidentity.Codex].Runtime.Generation()
	require.NoError(t, registry.Restart(runtimeidentity.Claude))
	require.Equal(t, generation, registry.entries[runtimeidentity.Codex].Runtime.Generation())
	reconnected := connectRuntimeSSHWithOptions(t, ctx, connections[runtimeidentity.Claude], runtimeidentity.Claude, codex.SocketClientOptions{ClientName: "Codex Desktop", ServerRequestHandler: fixture.callback(t, runtimeidentity.Claude)})
	readSessionThread(t, ctx, reconnected, "thread/resume", map[string]any{"threadId": threads[runtimeidentity.Claude]})
	retried := nativeMetadataCall[struct{ Turn struct{ ID string } }](t, ctx, reconnected, "turn/start", params(runtimeidentity.Claude))
	require.Equal(t, turns[runtimeidentity.Claude], retried.Turn.ID)
	for _, engine := range engines {
		data, err := os.ReadFile(filepath.Join(fixture.root, string(engine)+"-effect.txt"))
		require.NoError(t, err)
		require.Equal(t, fixture.secret+"-"+string(engine), string(data), "重启和重复提交不得复用或重放另一引擎副作用")
		require.Equal(t, int64(1), fixture.tools[engine].Load())
	}
}
