//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/interactiveprotocol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// 固定官方 CLI 与真实 MCP SDK 执行表单，Control 回答后检查原生结果和文件。
func TestWorkerControlCodexFormsRealSSH(t *testing.T) {
	requireControlNetworkIsolation(t)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	type scenario struct{ mode, action string }
	var cases []scenario
	for _, mode := range []string{"form", "openai/form", "openaiForm"} {
		for _, action := range []string{"accept", "decline", "cancel"} {
			cases = append(cases, scenario{mode, action})
		}
	}
	var calls atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		require.NoError(t, err)
		var request struct {
			Input []struct {
				Role, Type      string
				CallID          string `json:"call_id"`
				Content, Output json.RawMessage
			}
			Tools []struct {
				Type, Name string
				Tools      []struct{ Name string }
			}
			Text struct{ Format struct{ Type string } }
		}
		require.NoError(t, json.Unmarshal(body, &request))
		if request.Text.Format.Type == "json_schema" {
			bootstrapEvent(w, "response.created", map[string]any{"response": map[string]any{"id": "form-title"}})
			bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
				"id": "form-title-message", "type": "message", "role": "assistant",
				"content": []map[string]string{{"type": "output_text", "text": "{\"title\":\"原生表单验收\"}"}},
			}})
			bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": "form-title"}})
			return
		}
		require.LessOrEqual(t, calls.Add(1), int64(len(cases)*2))
		index := -1
		for _, input := range request.Input {
			if input.Role == "user" {
				for candidate := range cases {
					if strings.Contains(string(input.Content), fmt.Sprintf("MCP_TYPED_%d", candidate)) {
						index = candidate
					}
				}
			}
		}
		require.NotEqual(t, -1, index, "模型请求必须来自本次隔离表单场景")
		callID := fmt.Sprintf("mcp-typed-%d", index)
		for _, input := range request.Input {
			if input.Type != "function_call_output" || input.CallID != callID {
				continue
			}
			var parts []struct{ Type, Text string }
			require.NoError(t, json.Unmarshal(input.Output, &parts))
			var output strings.Builder
			for _, part := range parts {
				output.WriteString(part.Text)
			}
			var result struct {
				Action  string
				Content map[string]any
			}
			// 原生 MCP 输出可能带不可信内容说明；只解码服务实际返回的唯一结果。
			const marker = "MCP_RESULT "
			require.Equal(t, 1, strings.Count(output.String(), marker))
			_, raw, _ := strings.Cut(output.String(), marker)
			require.NoError(t, json.NewDecoder(strings.NewReader(raw)).Decode(&result))
			require.Equal(t, cases[index].action, result.Action)
			if result.Action == "accept" {
				require.Equal(t, map[string]any{"count": float64(2), "enabled": false}, result.Content)
			} else {
				require.Nil(t, result.Content)
			}
			bootstrapModelText(w, false)
			return
		}
		declared := false
		for _, group := range request.Tools {
			if group.Type == "namespace" && group.Name == "mcp__typedfixture" {
				for _, tool := range group.Tools {
					declared = declared || tool.Name == "confirm_typed"
				}
			}
		}
		require.True(t, declared, "只能调用官方 CLI 实际声明的 MCP 工具")
		bootstrapEvent(w, "response.created", map[string]any{"response": map[string]any{"id": callID}})
		bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
			"type": "function_call", "namespace": "mcp__typedfixture", "name": "confirm_typed", "call_id": callID, "arguments": "{}",
		}})
		bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": callID}})
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	discord := startControlDiscordFixture(t, ctx, f)
	workerCtx, cancelWorker := context.WithCancel(ctx)
	app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Run(workerCtx) }()
	t.Cleanup(func() { cancelWorker(); <-done; cleanup() })
	entry, err := app.Runtimes.Entry(runtimeidentity.Codex)
	require.NoError(t, err)
	requests := make(chan codex.ServerRequest, len(cases))
	client, _ := connectBootstrapSSHWithOptions(t, ctx, entry, f.signer, codex.SocketClientOptions{
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			var params map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(request.Params, &params))
			var meta map[string]any
			_ = json.Unmarshal(params["_meta"], &meta)
			if meta["codex_approval_kind"] == "mcp_tool_call" {
				// 原生先请求工具执行授权；真正的 MCP 表单另行由 Discord 回答。
				return map[string]any{"action": "accept", "content": map[string]any{}, "_meta": nil}, nil
			}
			requests <- request
			// 早到的 SSH 答案类型错误，不能抢占随后合法的 Discord 答案。
			return map[string]any{"action": "accept", "content": map[string]any{"count": "2", "enabled": false}}, nil
		},
	})
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	// 正式矩阵从仓库根运行测试二进制，不能依赖 go test 的包目录。
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	fixture := filepath.Join(filepath.Dir(source), "../../tools/protocol-fixtures/mcp-openai-form.mjs")
	require.FileExists(t, fixture)
	adapter := filepath.Dir(filepath.Dir(os.Getenv("TYRS_HAND_TEST_CLAUDE_BIN")))
	manager := discordintegration.NewManager(f.db, nil)
	var evidence []map[string]any
	for index, test := range cases {
		t.Logf("真实 Codex 表单 %s/%s", test.mode, test.action)
		path := filepath.Join(f.cfg.WorkerWorkspaceRoot, fmt.Sprintf("typed-form-%d.jsonl", index))
		var started struct{ Thread struct{ ID string } }
		require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
			"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "on-request", "sandbox": "danger-full-access",
			"config": map[string]any{"mcp_servers": map[string]any{"typedfixture": map[string]any{
				"command": node, "args": []string{fixture, adapter, test.mode, path},
			}}},
		}, &started))
		threadID := started.Thread.ID
		events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
		t.Cleanup(events.Close)
		var turn struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{
			"threadId": threadID, "input": []map[string]string{{"type": "text", "text": fmt.Sprintf("MCP_TYPED_%d", index)}},
		}, &turn))
		var request codex.ServerRequest
		select {
		case request = <-requests:
		case <-time.After(20 * time.Second):
			t.Fatal("原生 MCP 表单未到达 SSH")
		}
		require.Equal(t, interactiveprotocol.MCPElicitation, request.Method)
		var params struct{ Mode, ThreadID, TurnID string }
		require.NoError(t, json.Unmarshal(request.Params, &params))
		require.Equal(t, test.mode, params.Mode)
		require.Equal(t, threadID, params.ThreadID)
		require.Equal(t, turn.Turn.ID, params.TurnID)
		var id uuid.UUID
		var storedParams json.RawMessage
		discord.deliverUntil(t, ctx, func() bool {
			return f.db.QueryRowContext(ctx, "SELECT id,request_params FROM codex_interactive_requests WHERE thread_id=$1 AND app_server_request_id=$2::jsonb AND status='pending' AND discord_message_id IS NOT NULL", threadID, request.ID).Scan(&id, &storedParams) == nil
		})
		require.JSONEq(t, string(request.Params), string(storedParams))
		require.NoFileExists(t, path)
		option := map[string]int{"accept": 0, "decline": 1, "cancel": 2}[test.action]
		answer, err := manager.AnswerInteractive(ctx, f.guildID, id, 0, option, "")
		require.NoError(t, err)
		if test.action == "accept" {
			require.False(t, answer.Complete)
			_, err = manager.AnswerInteractive(ctx, f.guildID, id, 1, -1, "0")
			require.Error(t, err, "字段值必须遵循原生 schema")
			answer, err = manager.AnswerInteractive(ctx, f.guildID, id, 1, -1, "2")
			require.NoError(t, err)
			require.False(t, answer.Complete)
			answer, err = manager.AnswerInteractive(ctx, f.guildID, id, 2, 1, "")
			require.NoError(t, err)
		}
		require.True(t, answer.Complete)
		watcher := channelsTurnWatcher{events: events}
		watcher.awaitCompleted(t, ctx, turn.Turn.ID, nil)
		awaitControlRunCount(t, ctx, f, runtimeidentity.Codex, index+1)
		var native json.RawMessage
		require.NoError(t, f.db.QueryRowContext(ctx, "SELECT answer FROM codex_interactive_requests WHERE id=$1", id).Scan(&native))
		if test.action == "accept" {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, 1, strings.Count(string(data), "\n"), "只允许一个有效答案写一次文件")
		} else {
			require.NoFileExists(t, path)
		}
		evidence = append(evidence, map[string]any{"mode": params.Mode, "action": test.action,
			"turnId": turn.Turn.ID, "requestId": id, "nativeParams": request.Params, "nativeAnswer": native})
	}
	require.EqualValues(t, len(cases)*2, calls.Load(), "每个工具结果只回模一次")
	discord.deliverUntil(t, ctx, func() bool {
		var pending int
		return f.db.QueryRowContext(ctx, "SELECT count(*) FROM integration_outbox WHERE status<>'completed'").Scan(&pending) == nil && pending == 0
	})
	saveBootstrapArtifact(t, "mcp-form-effects", runtimeidentity.Codex, map[string]any{"cases": evidence})
}
