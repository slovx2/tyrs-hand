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

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeCodexErrorRealSSH(t *testing.T) { testRuntimeRegistryRealSSH(t, "codex-error") }

type runtimeCodexErrorFixture struct {
	root, marker string
	calls        atomic.Int64
}

func (f *runtimeCodexErrorFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	t.Helper()
	require.Equal(t, "/v1/responses", request.URL.Path)
	step := f.calls.Add(1)
	require.LessOrEqual(t, step, int64(5), "错误后的工具副作用不能重放")
	if step == 1 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
			"type": "invalid_request_error", "code": "invalid_request_error", "message": "EVENTS_011_HTTP_400"}}))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		data, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
		require.NoError(t, err)
		w.(http.Flusher).Flush()
	}
	id := fmt.Sprintf("native-error-%d", step)
	if step == 2 || step == 3 {
		event("response.created", map[string]any{"response": map[string]any{"id": id}})
		// 真实 SSE 在 response.completed 前 EOF，由 CLI 自行产生重试及终态错误。
		return
	}
	var parsed struct {
		Tools []struct{ Name string }
		Input []struct {
			Type   string
			CallID string `json:"call_id"`
			Output string
		}
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	if step == 5 {
		found := false
		for _, input := range parsed.Input {
			if input.Type == "function_call_output" && input.CallID == "native-error-recovery" {
				require.Contains(t, input.Output, f.marker)
				found = true
			}
		}
		require.True(t, found, "恢复回合的真实工具输出必须回到模型")
		contents, err := os.ReadFile(filepath.Join(f.root, "project", "error-recovery.txt"))
		require.NoError(t, err)
		require.Equal(t, f.marker+"\n", string(contents), "恢复工具只能执行一次")
		runtimeTextModel(w, request, "EVENTS_011_RECOVERED", id)
		return
	}
	declared := false
	for _, tool := range parsed.Tools {
		declared = declared || tool.Name == "exec_command"
	}
	require.True(t, declared, "必须执行固定 CLI 真实声明的 exec_command")
	args, err := json.Marshal(map[string]any{"cmd": "printf '" + f.marker + "\\n' >> error-recovery.txt; cat error-recovery.txt",
		"workdir": filepath.Join(f.root, "project"), "login": false})
	require.NoError(t, err)
	event("response.created", map[string]any{"response": map[string]any{"id": id}})
	event("response.output_item.done", map[string]any{"item": map[string]any{"type": "function_call", "name": "exec_command",
		"call_id": "native-error-recovery", "arguments": string(args)}})
	event("response.completed", map[string]any{"response": map[string]any{"id": id,
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}})
}

type runtimeNativeError struct {
	Message           string
	CodexErrorInfo    json.RawMessage
	AdditionalDetails *string
}

type runtimeErrorTurnEvidence struct {
	Scenario, ThreadID, TurnID, Status string
	Sequence                           []string
	Errors                             []runtimeNativeError
	WillRetry                          []bool
	HistoryError                       *runtimeNativeError
	ModelRequests                      int64
}

func runtimeErrorWait(t *testing.T, ctx context.Context, events *codex.EventSubscription,
	threadID, turnID, scenario string,
) runtimeErrorTurnEvidence {
	t.Helper()
	result := runtimeErrorTurnEvidence{Scenario: scenario, ThreadID: threadID, TurnID: turnID}
	started := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal("等待真实 Codex 错误及终态超时")
		case event, ok := <-events.Events():
			require.True(t, ok)
			var params struct {
				ThreadID, TurnID string
				WillRetry        bool
				Error            runtimeNativeError
				Turn             struct {
					ID, Status string
					Error      *runtimeNativeError
				}
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			switch event.Method {
			case "turn/started":
				require.Equal(t, threadID, params.ThreadID)
				require.Equal(t, turnID, params.Turn.ID)
				require.False(t, started)
				started = true
				result.Sequence = append(result.Sequence, event.Method)
			case "error":
				require.True(t, started, "error 必须晚于当前 Turn 开始")
				require.Equal(t, threadID, params.ThreadID)
				require.Equal(t, turnID, params.TurnID)
				require.NotEmpty(t, params.Error.Message)
				result.Sequence = append(result.Sequence, fmt.Sprintf("error:willRetry=%t", params.WillRetry))
				result.Errors = append(result.Errors, params.Error)
				result.WillRetry = append(result.WillRetry, params.WillRetry)
			case "turn/completed":
				require.True(t, started)
				require.Equal(t, threadID, params.ThreadID)
				require.Equal(t, turnID, params.Turn.ID)
				result.Sequence = append(result.Sequence, event.Method)
				result.Status = params.Turn.Status
				if scenario == "recover" {
					require.Equal(t, "completed", params.Turn.Status)
					require.Empty(t, result.Errors)
					require.Nil(t, params.Turn.Error)
				} else {
					require.Equal(t, "failed", params.Turn.Status)
					require.NotEmpty(t, result.Errors)
					require.NotNil(t, params.Turn.Error)
					require.Equal(t, result.Errors[len(result.Errors)-1], *params.Turn.Error)
				}
				return result
			}
		}
	}
}

func verifyRuntimeCodexError(t *testing.T, ctx context.Context, registry *RuntimeRegistry,
	connections map[runtimeidentity.Engine]*ssh.Client, fixture *runtimeCodexErrorFixture,
) {
	t.Helper()
	otherGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	client, trace := connectRuntimeSSHWithTrace(t, ctx, connections[runtimeidentity.Codex], runtimeidentity.Codex, codex.SocketClientOptions{})
	other, otherTrace := connectRuntimeSSHWithTrace(t, ctx, connections[runtimeidentity.Claude], runtimeidentity.Claude, codex.SocketClientOptions{})
	var otherThreads struct{ Data []json.RawMessage }
	require.NoError(t, other.Call(ctx, "thread/list", map[string]any{}, &otherThreads))
	require.Empty(t, otherThreads.Data)
	project := filepath.Join(fixture.root, "project")
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
		"cwd": project, "approvalPolicy": "never", "sandbox": "danger-full-access"})
	var evidence []runtimeErrorTurnEvidence
	for index, scenario := range []string{"http400", "sse-eof", "recover"} {
		events := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
		var started struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread.ID,
			"input": []map[string]any{{"type": "text", "text": "EVENTS_011_" + scenario}}}, &started))
		result := runtimeErrorWait(t, ctx, events, thread.ID, started.Turn.ID, scenario)
		events.Close()
		result.ModelRequests = fixture.calls.Load()
		switch scenario {
		case "http400":
			require.Equal(t, []bool{false}, result.WillRetry, "真实 HTTP400 不应重试")
			require.Equal(t, int64(1), result.ModelRequests)
			require.Contains(t, result.Errors[0].Message, "EVENTS_011_HTTP_400")
			require.JSONEq(t, `"other"`, string(result.Errors[0].CodexErrorInfo), "保持固定 CLI 的真实分类，不伪造 badRequest")
		case "sse-eof":
			require.Equal(t, []bool{true, false}, result.WillRetry, "真实流断开必须先重试，再宣布最终失败")
			require.Equal(t, int64(3), result.ModelRequests, "willRetry=true 必须对应第二次真实 HTTP 请求")
			require.JSONEq(t, `{"responseStreamDisconnected":{"httpStatusCode":null}}`, string(result.Errors[0].CodexErrorInfo))
			require.NotNil(t, result.Errors[0].AdditionalDetails)
			require.Contains(t, *result.Errors[0].AdditionalDetails, "stream closed before response.completed")
			require.JSONEq(t, `"other"`, string(result.Errors[1].CodexErrorInfo))
			require.Contains(t, result.Errors[1].Message, "stream closed before response.completed")
		case "recover":
			require.Equal(t, int64(5), result.ModelRequests)
		}
		var history struct {
			Thread struct {
				Turns []struct {
					ID, Status string
					Error      *runtimeNativeError
					Items      []struct{ Type, Text string }
				}
			}
		}
		require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true}, &history))
		require.Len(t, history.Thread.Turns, index+1)
		for previous := 0; previous < index; previous++ {
			require.Equal(t, evidence[previous].TurnID, history.Thread.Turns[previous].ID)
			require.Equal(t, evidence[previous].Status, history.Thread.Turns[previous].Status, "新回合不得抹去已有失败历史")
		}
		current := history.Thread.Turns[index]
		require.Equal(t, result.TurnID, current.ID)
		require.Equal(t, result.Status, current.Status)
		result.HistoryError = current.Error
		if scenario != "recover" {
			require.NotNil(t, current.Error)
			require.Equal(t, result.Errors[len(result.Errors)-1], *current.Error)
			_, err := os.Stat(filepath.Join(project, "error-recovery.txt"))
			require.True(t, os.IsNotExist(err), "失败回合不得提前执行恢复工具")
		} else {
			require.Nil(t, current.Error)
			found := false
			for _, item := range current.Items {
				found = found || item.Type == "agentMessage" && item.Text == "EVENTS_011_RECOVERED"
			}
			require.True(t, found, "显式恢复回合必须形成原生历史")
		}
		evidence = append(evidence, result)
	}
	contents, err := os.ReadFile(filepath.Join(project, "error-recovery.txt"))
	require.NoError(t, err)
	require.Equal(t, fixture.marker+"\n", string(contents))
	require.NoError(t, other.Call(ctx, "thread/list", map[string]any{}, &otherThreads))
	require.Empty(t, otherThreads.Data, "Codex 错误和恢复不得在 Claude 创建会话")
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
	verifyRuntimeErrorTrace(t, trace, otherTrace, evidence)
	if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
		data, err := json.MarshalIndent(map[string]any{"formatVersion": 1, "runId": os.Getenv("PROTOCOL_RUN_ID"),
			"engine": "codex", "caseName": t.Name(), "caseIds": []string{"EVENTS-011"}, "kind": "event-effects",
			"payload": map[string]any{"turns": evidence, "fileContent": string(contents), "modelCalls": fixture.calls.Load(),
				"claudeGenerationUnchanged": true, "claudeThreadsEmpty": true, "claudeErrorEvents": 0}}, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(directory, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "effects-codex-error.json"), data, 0o600))
	}
}

func verifyRuntimeErrorTrace(t *testing.T, trace, other *protocolTraceTransport, evidence []runtimeErrorTurnEvidence) {
	t.Helper()
	trace.mu.Lock()
	defer trace.mu.Unlock()
	for _, turn := range evidence {
		var starts, completions, errors int
		for _, message := range trace.messages {
			params, _ := message["params"].(map[string]any)
			if params["threadId"] != turn.ThreadID {
				continue
			}
			switch message["method"] {
			case "error":
				if params["turnId"] == turn.TurnID {
					require.Zero(t, completions, "error 不能迟于失败完成通知")
					errors++
				}
			case "turn/started", "turn/completed":
				value, _ := params["turn"].(map[string]any)
				if value["id"] == turn.TurnID {
					if message["method"] == "turn/started" {
						starts++
					} else {
						completions++
					}
				}
			}
		}
		require.Equal(t, 1, starts)
		require.Equal(t, 1, completions)
		require.Equal(t, len(turn.Errors), errors, "失败完成后不能再补发相同错误")
	}
	other.mu.Lock()
	defer other.mu.Unlock()
	for _, message := range other.messages {
		require.NotEqual(t, "error", message["method"], "Codex 错误不能发送至 Claude SSH 客户端")
		require.NotEqual(t, "turn/started", message["method"])
		require.NotEqual(t, "turn/completed", message["method"])
		params, _ := message["params"].(map[string]any)
		require.NotEqual(t, evidence[0].ThreadID, params["threadId"], "其他会话通知也不得跨引擎泄漏")
		thread, _ := params["thread"].(map[string]any)
		require.NotEqual(t, evidence[0].ThreadID, thread["id"])
	}
}
