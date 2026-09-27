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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// 真实 PTY 保留上一回合权限；降为只读后必须对写入 stdin 单独审批。
func TestWorkerControlCodexStdinApprovalRealSSH(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var sessionID, modelCalls atomic.Int64
	var target string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		require.NoError(t, err)
		var request struct {
			Input []map[string]json.RawMessage
			Tools []struct{ Name string }
			Text  struct{ Format struct{ Type string } }
		}
		require.NoError(t, json.Unmarshal(body, &request))
		if request.Text.Format.Type == "json_schema" {
			bootstrapEvent(w, "response.created", map[string]any{"response": map[string]any{"id": "stdin-title"}})
			bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
				"id": "stdin-title-message", "type": "message", "role": "assistant",
				"content": []map[string]string{{"type": "output_text", "text": "{\"title\":\"终端输入审批\"}"}},
			}})
			bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": "stdin-title"}})
			return
		}
		require.LessOrEqual(t, modelCalls.Add(1), int64(5), "工具不得重复请求或重放")
		stage := ""
		outputs := map[string]string{}
		for _, item := range request.Input {
			var role, callID, output string
			_ = json.Unmarshal(item["role"], &role)
			if role == "user" {
				for _, candidate := range []string{"OPEN", "CANCEL", "ACCEPT"} {
					if strings.Contains(string(item["content"]), "STDIN_"+candidate) {
						stage = candidate
					}
				}
			}
			_ = json.Unmarshal(item["call_id"], &callID)
			if json.Unmarshal(item["output"], &output) == nil && callID != "" {
				outputs[callID] = output
			}
		}
		require.NotEmpty(t, stage)
		callID := "stdin-" + strings.ToLower(stage)
		if output := outputs[callID]; output != "" {
			switch stage {
			case "OPEN":
				match := regexp.MustCompile("Process running with session ID ([0-9]+)").FindStringSubmatch(output)
				require.Len(t, match, 2, output)
				id, err := strconv.ParseInt(match[1], 10, 64)
				require.NoError(t, err)
				sessionID.Store(id)
			case "CANCEL":
				t.Fatal("原生取消回合后不得产生模型续写")
			case "ACCEPT":
				require.Contains(t, output, "Process exited with code 0")
			}
			bootstrapModelText(w, false)
			return
		}
		tool := "write_stdin"
		args := map[string]any{"session_id": sessionID.Load(), "chars": "REJECTED=1\n", "yield_time_ms": 1000}
		switch stage {
		case "OPEN":
			tool = "exec_command"
			args = map[string]any{"cmd": "/bin/bash --noprofile --norc", "tty": true, "yield_time_ms": 200}
		case "ACCEPT":
			args["chars"] = "test -z \"$REJECTED\" && printf 'STDIN_ACCEPTED\\n' > '" + target + "'\nexit\n"
		}
		declared := false
		for _, candidate := range request.Tools {
			declared = declared || candidate.Name == tool
		}
		require.True(t, declared, "只能调用官方 CLI 实际声明的工具")
		encoded, err := json.Marshal(args)
		require.NoError(t, err)
		bootstrapEvent(w, "response.created", map[string]any{"response": map[string]any{"id": callID}})
		bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
			"type": "function_call", "name": tool, "call_id": callID, "arguments": string(encoded),
		}})
		bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": callID}})
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	target = filepath.Join(f.cfg.WorkerWorkspaceRoot, "stdin-accepted.txt")
	discord := startControlDiscordFixture(t, ctx, f)
	workerCtx, cancelWorker := context.WithCancel(ctx)
	app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Run(workerCtx) }()
	t.Cleanup(func() { cancelWorker(); <-done; cleanup() })
	entry, err := app.Runtimes.Entry(runtimeidentity.Codex)
	require.NoError(t, err)
	prompts := make(chan codex.ServerRequest, 4)
	client, _ := connectBootstrapSSHWithOptions(t, ctx, entry, f.signer, codex.SocketClientOptions{
		ServerRequestHandler: func(ctx context.Context, request codex.ServerRequest) (any, error) {
			select {
			case prompts <- request:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	var started struct{ Thread struct{ ID string } }
	require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
		"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "never", "sandbox": "danger-full-access",
		"config": map[string]any{"features.write_stdin_approval": true},
	}, &started))
	threadID := started.Thread.ID
	events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(events.Close)
	watcher := channelsTurnWatcher{events: events}
	start := func(stage string) string {
		params := map[string]any{"threadId": threadID,
			"input": []map[string]string{{"type": "text", "text": "STDIN_" + stage}}}
		if stage != "OPEN" {
			params["approvalPolicy"] = "on-request"
			params["sandboxPolicy"] = map[string]any{"type": "readOnly"}
		}
		var turn struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", params, &turn))
		return turn.Turn.ID
	}
	watcher.awaitCompleted(t, ctx, start("OPEN"), nil)
	require.Positive(t, sessionID.Load())
	awaitControlRunCount(t, ctx, f, runtimeidentity.Codex, 1)
	manager := discordintegration.NewManager(f.db, nil)
	var parentItem string
	for _, stage := range []string{"CANCEL", "ACCEPT"} {
		turnID := start(stage)
		var request codex.ServerRequest
		select {
		case request = <-prompts:
		case <-ctx.Done():
			t.Fatal("真实终端输入审批未到达 SSH")
		}
		require.Equal(t, "item/commandExecution/requestApproval", request.Method)
		var params struct{ Kind, ThreadID, TurnID, ItemID, ApprovalID, Command string }
		require.NoError(t, json.Unmarshal(request.Params, &params))
		require.Equal(t, "writeStdin", params.Kind)
		require.Equal(t, threadID, params.ThreadID)
		require.Equal(t, turnID, params.TurnID)
		require.Equal(t, "stdin-"+strings.ToLower(stage), params.ApprovalID)
		if parentItem == "" {
			parentItem = params.ItemID
		}
		require.Equal(t, parentItem, params.ItemID, "两个回调引用同一个真实终端命令")
		require.NoFileExists(t, target, "用户回答前不可写入终端")
		var id uuid.UUID
		var storedParams, questions json.RawMessage
		discord.deliverUntil(t, ctx, func() bool {
			query := "SELECT id,request_params,questions FROM codex_interactive_requests WHERE thread_id=$1 AND app_server_request_id=$2::jsonb AND status='pending' AND discord_message_id IS NOT NULL"
			return f.db.QueryRowContext(ctx, query, threadID, request.ID).Scan(&id, &storedParams, &questions) == nil
		})
		require.JSONEq(t, string(request.Params), string(storedParams), "审计必须保留 kind、approvalId 和真实输入")
		require.Contains(t, string(questions), "终端输入审批")
		option, decision := 1, "cancel"
		if stage == "ACCEPT" {
			option, decision = 0, "accept"
		}
		answer, err := manager.AnswerInteractive(ctx, f.guildID, id, 0, option, "")
		require.NoError(t, err)
		require.True(t, answer.Complete)
		require.Contains(t, fmt.Sprint(answer.Card.Sections), "终端输入审批")
		require.NoError(t, discordintegration.ProjectInteractiveRequest(ctx, f.db, id))
		if stage == "CANCEL" {
			awaitStdinCanceledTurn(t, ctx, events, turnID)
		} else {
			watcher.awaitCompleted(t, ctx, turnID, nil)
		}
		wantStatus := "completed"
		if stage == "CANCEL" {
			wantStatus = "canceled"
		}
		require.Eventually(t, func() bool {
			var status string
			query := "SELECT status FROM codex_turn_runs WHERE confirmed_codex_turn_id=$1"
			return f.db.QueryRowContext(ctx, query, turnID).Scan(&status) == nil && status == wantStatus
		}, 15*time.Second, 50*time.Millisecond, "Control 终态必须对应原生回合")
		var recorded json.RawMessage
		require.NoError(t, f.db.QueryRowContext(ctx, "SELECT answer FROM codex_interactive_requests WHERE id=$1", id).Scan(&recorded))
		expected, _ := json.Marshal(map[string]string{"decision": decision})
		require.JSONEq(t, string(expected), string(recorded))
		if stage == "CANCEL" {
			require.NoFileExists(t, target)
		}
	}
	content, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "STDIN_ACCEPTED\n", string(content), "拒绝输入不能设置变量，允许输入只执行一次")
	require.EqualValues(t, 5, modelCalls.Load())
}

func awaitStdinCanceledTurn(t *testing.T, ctx context.Context, events *codex.EventSubscription, turnID string) {
	t.Helper()
	for {
		select {
		case event, ok := <-events.Events():
			require.True(t, ok, "原生中断终态前事件流不可关闭")
			if event.Method != "turn/completed" {
				continue
			}
			var params struct{ Turn struct{ ID, Status string } }
			require.NoError(t, json.Unmarshal(event.Params, &params))
			if params.Turn.ID == turnID {
				require.Equal(t, "interrupted", params.Turn.Status)
				return
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
