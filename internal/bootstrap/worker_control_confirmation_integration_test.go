//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// FAILURE-009：真实 SSH 创建 Claude 任务，原生确认先于 Control 登记仍须可靠补报。
func TestWorkerControlRemoteConfirmationAfterRegistrationRealSSH(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var mu sync.Mutex
	var requests []json.RawMessage
	var businessCalls int
	var createSent, created, runNowSent, runNowResultSeen, bashSent, bashResultSeen bool
	var scheduleID uuid.UUID
	var path string
	marker := "CONFIRMATION_" + uuid.NewString()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "count_tokens") {
			_, _ = io.WriteString(w, `{"input_tokens":10}`)
			return
		}
		if request.URL.Path != "/v1/messages" {
			http.NotFound(w, request)
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, 8<<20))
		require.NoError(t, err)
		var payload controlAutomationPayload
		require.NoError(t, json.Unmarshal(body, &payload))
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, body)
		var hasAutomationTool bool
		for _, tool := range payload.Tools {
			hasAutomationTool = hasAutomationTool || strings.Contains(tool.Description, "[tyrs_hand.automation_update]")
		}
		// 原生后台标题请求也保留证据，但不能当成任务重放或修改业务场景状态。
		if !hasAutomationTool {
			bootstrapModelText(w, true)
			return
		}
		businessCalls++
		input := automationLatestInput(payload)
		if strings.LastIndex(input, marker+"_RUN_NOW") > strings.LastIndex(input, "<scheduled_task>") {
			if !runNowSent {
				for _, tool := range payload.Tools {
					if strings.Contains(tool.Description, "[tyrs_hand.automation_update]") {
						runNowSent = true
						automationToolResponse(w, "toolu_confirmation_run_now", tool.Name, map[string]any{
							"action": "run_now", "task_id": scheduleID.String()})
						return
					}
				}
				t.Error("真实 Claude SDK 未声明平台调度工具")
			}
			_, found, failed := automationToolResult(payload, "toolu_confirmation_run_now")
			runNowResultSeen = found && !failed
			bootstrapModelText(w, true)
			return
		}
		if strings.Contains(input, "<scheduled_task>") {
			if !bashSent {
				bashSent = true
				quotedPath := "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
				automationToolResponse(w, "toolu_confirmation_bash", "Bash", map[string]any{
					"command": "printf '" + marker + "\\n' >> " + quotedPath + "; cat " + quotedPath})
				return
			}
			result, found, failed := automationToolResult(payload, "toolu_confirmation_bash")
			bashResultSeen = found && !failed && strings.Contains(string(result), marker)
			bootstrapModelText(w, true)
			return
		}
		if !createSent {
			for _, tool := range payload.Tools {
				if strings.Contains(tool.Description, "[tyrs_hand.automation_update]") {
					createSent = true
					automationToolResponse(w, "toolu_confirmation_create", tool.Name, map[string]any{
						"action": "create", "kind": "heartbeat", "name": marker, "prompt": marker,
						"schedule": "DTSTART:20300102T000000Z\nRRULE:FREQ=DAILY"})
					return
				}
			}
			t.Error("真实 Claude SDK 未声明平台调度工具")
		}
		_, found, failed := automationToolResult(payload, "toolu_confirmation_create")
		created = found && !failed
		bootstrapModelText(w, true)
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	path = filepath.Join(f.cfg.WorkerWorkspaceRoot, "confirmation-effect.txt")
	gate, rejected := newConfirmationRegistrationGate(t, f.cfg.WorkerControlURL)
	f.cfg.WorkerControlURL = gate.URL
	workerCtx, stopWorker := context.WithCancel(ctx)
	app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Run(workerCtx) }()
	t.Cleanup(func() { stopWorker(); <-done; cleanup() })
	entry, err := app.Runtimes.Entry(runtimeidentity.Claude)
	require.NoError(t, err)
	client, _ := connectBootstrapSSH(t, ctx, entry, f.signer)
	var started struct{ Thread struct{ ID string } }
	require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
		"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "never", "sandbox": "danger-full-access",
	}, &started))
	events := client.Subscribe(codex.ThreadFilter{ThreadID: started.Thread.ID})
	t.Cleanup(events.Close)
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": started.Thread.ID,
		"input": []map[string]any{{"type": "text", "text": marker, "text_elements": []any{}}}}, nil))
	awaitBootstrapTurn(t, ctx, events)
	awaitControlRunCount(t, ctx, f, runtimeidentity.Claude, 1)
	mu.Lock()
	require.True(t, created, "创建任务工具的真实结果必须回到模型")
	mu.Unlock()
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT id FROM scheduled_tasks
		WHERE workspace_id=$1 AND name=$2 AND engine='claude-code'`, f.workspaceID, marker).Scan(&scheduleID))
	var runNow struct{ Turn struct{ ID string } }
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": started.Thread.ID,
		"input": []map[string]any{{"type": "text", "text": marker + "_RUN_NOW", "text_elements": []any{}}}}, &runNow))
	awaitBootstrapTurn(t, ctx, events)
	awaitControlRunCount(t, ctx, f, runtimeidentity.Claude, 3)
	mu.Lock()
	require.True(t, runNowResultSeen, "真实 run_now 工具结果必须回到发起回合的模型上下文")
	require.True(t, bashResultSeen, "Bash 实际输出必须回到同一 Claude 模型上下文")
	require.Equal(t, 6, businessCalls, "补登记不得重新执行业务模型或工具")
	mu.Unlock()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, marker+"\n", string(contents), "真实 Bash 副作用必须恰好一次")
	require.Positive(t, rejected.Load(), "必须先观测真实 Control 的确认 404，再放行登记")
	var confirmation, submission, runConfirmation, status string
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT i.confirmed_codex_turn_id,i.codex_submission_id,
		r.confirmed_codex_turn_id,s.status FROM scheduled_task_runs s
		JOIN codex_turn_intents i ON i.id=s.intent_id JOIN codex_turn_runs r ON r.primary_intent_id=i.id
		WHERE s.scheduled_task_id=$1`, scheduleID).Scan(&confirmation, &submission, &runConfirmation, &status))
	require.Equal(t, "succeeded", status)
	require.NotEmpty(t, submission)
	require.NotEmpty(t, confirmation)
	require.NotEqual(t, runNow.Turn.ID, confirmation, "排队的任务必须在 run_now 发起回合结束后启动独立原生回合")
	require.Equal(t, confirmation, runConfirmation)
	var history struct {
		Thread struct{ Turns []struct{ ID string } }
	}
	require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": started.Thread.ID, "includeTurns": true}, &history))
	require.Len(t, history.Thread.Turns, 3)
	require.Equal(t, history.Thread.Turns[2].ID, confirmation, "持久化确认必须等于真实原生历史的 Turn ID")
	mu.Lock()
	saveBootstrapArtifact(t, "models", runtimeidentity.Claude, map[string]any{"requests": requests})
	modelCalls := len(requests)
	mu.Unlock()
	saveBootstrapArtifact(t, "effects", runtimeidentity.Claude, map[string]any{"confirmation": confirmation,
		"initialConfirmation404": rejected.Load(), "file": string(contents), "modelCalls": modelCalls, "businessModelCalls": 6})
}
