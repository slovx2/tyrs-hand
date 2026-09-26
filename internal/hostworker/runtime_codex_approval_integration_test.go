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
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeCodexApprovalsRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-approvals")
}

type runtimeCodexApprovalFixture struct {
	root    string
	calls   atomic.Int64
	outputs sync.Map
}

var codexApprovalScenarios = []string{
	"question-answer", "question-cancel", "patch-accept", "patch-decline", "patch-cancel",
	"command-accept", "command-decline", "command-cancel",
}

func (f *runtimeCodexApprovalFixture) model(t *testing.T, w http.ResponseWriter, body []byte) {
	t.Helper()
	require.LessOrEqual(t, f.calls.Add(1), int64(13), "取消后不能续写，工具不能重放")
	var request struct {
		Tools []struct{ Name string }
		Input []struct {
			Role, Type string
			CallID     string `json:"call_id"`
			Content    json.RawMessage
			Output     json.RawMessage
		}
	}
	require.NoError(t, json.Unmarshal(body, &request))
	scenario := ""
	for _, input := range request.Input {
		if input.Role != "user" {
			continue
		}
		for _, candidate := range codexApprovalScenarios {
			if strings.Contains(string(input.Content), "CODEX_APPROVAL_"+candidate) {
				scenario = candidate
			}
		}
	}
	require.NotEmpty(t, scenario, "只能接受已登记的真实模型请求")
	id := "codex-approval-" + scenario
	output := ""
	for _, input := range request.Input {
		if input.CallID == id && (input.Type == "function_call_output" || input.Type == "custom_tool_call_output") {
			require.NoError(t, json.Unmarshal(input.Output, &output))
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
		require.NoError(t, err)
		w.(http.Flusher).Flush()
	}
	event("response.created", map[string]any{"response": map[string]any{"id": id}})
	if output != "" {
		require.False(t, strings.HasSuffix(scenario, "cancel"), "取消后不得产生模型续写")
		_, loaded := f.outputs.LoadOrStore(scenario, output)
		require.False(t, loaded, "每个工具结果只能入模一次")
		switch {
		case scenario == "question-answer":
			require.JSONEq(t, `{"answers":{"confirm":{"answers":["Yes"]}}}`, output)
		case strings.HasSuffix(scenario, "accept"):
			require.Contains(t, output, "Exit code: 0", "真实工具必须执行成功")
		case strings.HasPrefix(scenario, "patch"):
			require.Contains(t, output, "rejected", "补丁拒绝必须入模")
		default:
			require.Contains(t, output, "rejected by user", "命令拒绝必须入模")
		}
		event("response.output_item.done", map[string]any{"item": map[string]any{
			"type": "message", "role": "assistant", "id": "msg-" + id,
			"content": []map[string]string{{"type": "output_text", "text": "CODEX_APPROVAL_DONE"}},
		}})
	} else {
		tool := "shell_command"
		if strings.HasPrefix(scenario, "question") {
			tool = "request_user_input"
		} else if strings.HasPrefix(scenario, "patch") {
			tool = "apply_patch"
		}
		declared := false
		for _, candidate := range request.Tools {
			declared = declared || candidate.Name == tool
		}
		require.True(t, declared, "只能发出固定 CLI 实际声明的工具")
		path := filepath.Join(f.root, "project", scenario+".txt")
		if tool == "apply_patch" {
			patch := "*** Begin Patch\n*** Add File: " + path + "\n+" + scenario + "\n*** End Patch\n"
			item := map[string]any{"type": "custom_tool_call", "name": tool, "id": "item-" + id, "call_id": id, "input": ""}
			event("response.output_item.added", map[string]any{"output_index": 0, "item": item})
			// 原生补丁流事件由 CLI 增量解析产生，不能用 shell 的终态补丁替代。
			parts := strings.SplitN(patch, "+", 2)
			for _, delta := range []string{parts[0], "+" + parts[1]} {
				event("response.custom_tool_call_input.delta", map[string]any{"item_id": item["id"], "output_index": 0, "delta": delta})
				time.Sleep(100 * time.Millisecond)
			}
			item["input"] = patch
			event("response.output_item.done", map[string]any{"output_index": 0, "item": item})
		} else {
			args := map[string]any{"command": "printf '" + scenario + "\\n' > " + path, "workdir": filepath.Join(f.root, "project")}
			if tool == "request_user_input" {
				args = map[string]any{"questions": []map[string]any{{"id": "confirm", "header": "Confirm", "question": "Continue?",
					"options": []map[string]string{{"label": "Yes", "description": "Continue"}, {"label": "No", "description": "Stop"}}}}}
			}
			encoded, err := json.Marshal(args)
			require.NoError(t, err)
			event("response.output_item.done", map[string]any{"item": map[string]any{
				"type": "function_call", "name": tool, "call_id": id, "arguments": string(encoded),
			}})
		}
	}
	event("response.completed", map[string]any{"response": map[string]any{"id": id,
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}})
}

// APPROVAL-008：完整访问下允许真实副作用；只读下拒绝/取消；计划提问回答/取消。
// 完全访问用例不代表受限 OS 沙箱允许执行通过，后者必须在 Linux 独立验收。
func verifyRuntimeCodexApprovals(t *testing.T, ctx context.Context, connection *ssh.Client, fixture *runtimeCodexApprovalFixture) {
	t.Helper()
	prompts := make(chan runtimeApprovalPrompt, 8)
	client := connectRuntimeSSHWithOptions(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{
		ServerRequestHandler: func(callbackCtx context.Context, request codex.ServerRequest) (any, error) {
			prompt := runtimeApprovalPrompt{request: request, reply: make(chan any, 1), cancelled: make(chan struct{})}
			select {
			case prompts <- prompt:
			case <-callbackCtx.Done():
				return nil, callbackCtx.Err()
			}
			select {
			case reply := <-prompt.reply:
				return reply, nil
			case <-callbackCtx.Done():
				close(prompt.cancelled)
				return nil, callbackCtx.Err()
			}
		},
	})
	for _, scenario := range codexApprovalScenarios {
		if !t.Run(scenario, func(t *testing.T) {
			sandbox := "read-only"
			if strings.HasSuffix(scenario, "accept") {
				sandbox = "danger-full-access"
			}
			thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
				"cwd": filepath.Join(fixture.root, "project"), "model": "gpt-5.4", "approvalPolicy": "untrusted", "sandbox": sandbox,
				// mock-model 没有原生 freeform apply_patch。模型只决定 CLI 工具目录，provider 始终是回环 Mock。
				"config": map[string]any{"features.unified_exec": false, "features.apply_patch_streaming_events": true},
			})
			events := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
			defer events.Close()
			params := map[string]any{"threadId": thread.ID, "input": []map[string]string{{"type": "text", "text": "CODEX_APPROVAL_" + scenario}}}
			if strings.HasPrefix(scenario, "question") {
				params["collaborationMode"] = map[string]any{"mode": "plan", "settings": map[string]any{
					"model": "gpt-5.4", "reasoning_effort": "medium", "developer_instructions": nil,
				}}
			}
			var started struct{ Turn struct{ ID string } }
			require.NoError(t, client.Call(ctx, "turn/start", params, &started))
			var prompt runtimeApprovalPrompt
			select {
			case prompt = <-prompts:
			case <-ctx.Done():
				t.Fatal("真实 Codex 审批未到达 SSH 客户端")
			}
			method := "item/commandExecution/requestApproval"
			if strings.HasPrefix(scenario, "patch") {
				method = "item/fileChange/requestApproval"
			} else if strings.HasPrefix(scenario, "question") {
				method = "item/tool/requestUserInput"
			}
			require.Equal(t, method, prompt.request.Method)
			var callback struct {
				ThreadID, TurnID, ItemID string
				Questions                []struct{ ID string }
			}
			require.NoError(t, json.Unmarshal(prompt.request.Params, &callback))
			require.Equal(t, thread.ID, callback.ThreadID)
			require.Equal(t, started.Turn.ID, callback.TurnID)
			require.NotEmpty(t, callback.ItemID)
			path := filepath.Join(fixture.root, "project", scenario+".txt")
			_, err := os.Stat(path)
			require.True(t, os.IsNotExist(err), "用户回答之前不能产生工具副作用")
			switch scenario {
			case "question-cancel":
				require.NoError(t, client.Call(ctx, "turn/interrupt", map[string]string{"threadId": thread.ID, "turnId": started.Turn.ID}, nil))
				select {
				case <-prompt.cancelled:
				case <-ctx.Done():
					t.Fatal("提问中断后客户端回调没有取消")
				}
			case "question-answer":
				require.Len(t, callback.Questions, 1)
				require.Equal(t, "confirm", callback.Questions[0].ID)
				prompt.reply <- map[string]any{"answers": map[string]any{"confirm": map[string]any{"answers": []string{"Yes"}}}}
			default:
				prompt.reply <- map[string]string{"decision": strings.SplitN(scenario, "-", 2)[1]}
			}
			verifyCodexApprovalTurn(t, ctx, events, thread.ID, started.Turn.ID, callback.ItemID, scenario, path)
			if strings.HasSuffix(scenario, "accept") {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, scenario+"\n", string(data), "用户允许后必须产生精确的真实文件副作用")
			} else {
				_, err := os.Stat(path)
				require.True(t, os.IsNotExist(err), "拒绝、取消和提问不能产生执行副作用")
			}
			_, continued := fixture.outputs.Load(scenario)
			require.Equal(t, !strings.HasSuffix(scenario, "cancel"), continued, "真实模型续写必须与审批终态一致")
		}) {
			return
		}
	}
	require.Equal(t, int64(13), fixture.calls.Load())
	select {
	case prompt := <-prompts:
		t.Fatalf("出现额外或重复审批: %s", prompt.request.Method)
	default:
	}
}

func verifyCodexApprovalTurn(t *testing.T, ctx context.Context, events *codex.EventSubscription, threadID, turnID, itemID, scenario, path string) {
	t.Helper()
	patchSeen, patchContentSeen := false, false
	for {
		select {
		case <-ctx.Done():
			t.Fatal("真实 Codex 审批 Turn 未结束")
		case event, ok := <-events.Events():
			require.True(t, ok)
			if event.Method != "item/fileChange/patchUpdated" && event.Method != "turn/completed" {
				continue
			}
			var params struct {
				ThreadID, TurnID, ItemID string
				Changes                  []struct{ Path, Diff string }
				Turn                     struct {
					ID, Status string
					Error      any
				}
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			require.Equal(t, threadID, params.ThreadID)
			if event.Method == "item/fileChange/patchUpdated" {
				require.True(t, strings.HasPrefix(scenario, "patch"))
				require.Equal(t, turnID, params.TurnID)
				require.Equal(t, itemID, params.ItemID)
				patchSeen = true
				for _, change := range params.Changes {
					require.Equal(t, path, change.Path)
					patchContentSeen = patchContentSeen || strings.Contains(change.Diff, scenario)
				}
				continue
			}
			require.Equal(t, turnID, params.Turn.ID)
			status := "completed"
			if strings.HasSuffix(scenario, "cancel") {
				status = "interrupted"
			}
			require.Equal(t, status, params.Turn.Status)
			require.Nil(t, params.Turn.Error)
			require.Equal(t, strings.HasPrefix(scenario, "patch"), patchSeen)
			require.Equal(t, strings.HasPrefix(scenario, "patch"), patchContentSeen, "真实增量补丁内容必须到达客户端")
			return
		}
	}
}
