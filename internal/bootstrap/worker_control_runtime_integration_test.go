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
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/scheduledtasks"
	"github.com/stretchr/testify/require"
)

// 真实 Control/PostgreSQL/Redis + 单 Worker + 双 SSH + 原生 CLI；只有模型服务是本地替身。
func TestWorkerControlRealSSHBothEngines(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 240*time.Second)
	defer cancel()
	var requestedTool, toolResultSeen, followupSeen atomic.Bool
	firstToolRequested := make(chan struct{})
	var mu sync.Mutex
	requests := map[runtimeidentity.Engine][]json.RawMessage{}
	approvals := newControlApprovalScenario()
	automation := &controlAutomationScenario{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "count_tokens") {
			_, _ = io.WriteString(w, `{"input_tokens":10}`)
			return
		}
		if req.URL.Path == "/api/hello" {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		if req.URL.Path != "/v1/messages" && req.URL.Path != "/v1/responses" {
			http.NotFound(w, req)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, 8<<20))
		if err != nil {
			t.Error(err)
			return
		}
		engine := runtimeidentity.Codex
		if req.URL.Path == "/v1/messages" {
			engine = runtimeidentity.Claude
		}
		mu.Lock()
		requests[engine] = append(requests[engine], body)
		mu.Unlock()
		if engine == runtimeidentity.Claude {
			if automation.respond(t, w, body) {
				return
			}
			if approvals.respond(t, w, body) {
				return
			}
			var payload struct {
				Tools    []struct{ Name, Description string }
				Messages json.RawMessage
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Error(err)
				return
			}
			var messages []struct {
				Role    string
				Content json.RawMessage
			}
			if json.Unmarshal(payload.Messages, &messages) == nil {
				for _, message := range messages {
					var blocks []struct {
						Type      string          `json:"type"`
						ToolUseID string          `json:"tool_use_id"`
						IsError   bool            `json:"is_error"`
						Content   json.RawMessage `json:"content"`
					}
					if json.Unmarshal(message.Content, &blocks) == nil {
						for _, block := range blocks {
							if block.Type == "tool_result" && block.ToolUseID == "toolu_control" {
								if block.IsError {
									t.Error("真实调度工具返回失败，不能仅在原始 tool_use 参数中寻找任务名")
								}
								toolResultSeen.Store(!block.IsError && strings.Contains(string(block.Content), "CONTROL_AUTOMATION"))
							}
						}
					}
					var text string
					if message.Role == "user" && json.Unmarshal(message.Content, &text) == nil &&
						strings.Contains(text, "<scheduled_task>") && strings.Contains(text, "CONTROL_AUTOMATION_FOLLOWUP") &&
						strings.Contains(string(payload.Messages), "CONTROL_SSH_FIRST") {
						followupSeen.Store(true)
					}
				}
			}
			for _, tool := range payload.Tools {
				if strings.Contains(tool.Description, "[tyrs_hand.automation_update]") && requestedTool.CompareAndSwap(false, true) {
					close(firstToolRequested)
					bootstrapClaudeStart(w)
					bootstrapEvent(w, "content_block_start", map[string]any{"index": 0, "content_block": map[string]any{
						"type": "tool_use", "id": "toolu_control", "name": tool.Name, "input": map[string]any{}}})
					bootstrapEvent(w, "content_block_delta", map[string]any{"index": 0, "delta": map[string]any{
						"type": "input_json_delta", "partial_json": `{"action":"create","kind":"heartbeat","name":"CONTROL_AUTOMATION","prompt":"CONTROL_AUTOMATION_FOLLOWUP","schedule":"DTSTART:20300102T000000Z\nRRULE:FREQ=DAILY"}`}})
					bootstrapClaudeEnd(w, "tool_use")
					return
				}
			}
		}
		bootstrapModelText(w, engine == runtimeidentity.Claude)
	}))
	t.Cleanup(model.Close)
	f := newControlRuntimeFixture(t, ctx, model.URL, firstToolRequested)
	discord := startControlDiscordFixture(t, ctx, f)
	startWorker := func() (*WorkerApp, func()) {
		workerCtx, cancelWorker := context.WithCancel(ctx)
		app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() { done <- app.Run(workerCtx) }()
		var once sync.Once
		stop := func() { once.Do(func() { cancelWorker(); <-done; cleanup() }) }
		t.Cleanup(stop)
		return app, stop
	}
	app, stopWorker := startWorker()
	threads := map[runtimeidentity.Engine]string{}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		entry, err := app.Runtimes.Entry(engine)
		require.NoError(t, err)
		client, _ := connectBootstrapSSH(t, ctx, entry, f.signer)
		var result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
			"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "never", "sandbox": "danger-full-access"}, &result))
		threads[engine] = result.Thread.ID
		subscription := client.Subscribe(codex.ThreadFilter{ThreadID: result.Thread.ID})
		t.Cleanup(subscription.Close)
		var turn any
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": result.Thread.ID,
			"input": []map[string]any{{"type": "text", "text": "CONTROL_SSH_FIRST", "text_elements": []any{}}}}, &turn))
		awaitBootstrapTurn(t, ctx, subscription)
		awaitControlRunCount(t, ctx, f, engine, 1)
	}
	require.True(t, requestedTool.Load(), "Claude 必须真正看到平台工具")
	require.True(t, toolResultSeen.Load(), "真实调度工具结果必须回到 SDK 下一轮请求")
	var scheduleID, sessionID, projectID, profileID uuid.UUID
	var scheduleEngine runtimeidentity.Engine
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT id,target_session_id,workspace_project_id,
		(SELECT agent_profile_id FROM workspace_sessions WHERE id=target_session_id),engine
		FROM scheduled_tasks WHERE workspace_id=$1 AND name='CONTROL_AUTOMATION'`, f.workspaceID).Scan(&scheduleID, &sessionID, &projectID, &profileID, &scheduleEngine))
	require.Equal(t, runtimeidentity.Claude, scheduleEngine)
	workerID := app.Runner.WorkerID()
	stopWorker()
	app, stopWorker = startWorker()
	require.Equal(t, workerID, app.Runner.WorkerID(), "重启不能注册第二个 Worker")
	require.Eventually(t, func() bool {
		var count int
		err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM worker_runtimes WHERE worker_id=$1 AND status='running'`, workerID).Scan(&count)
		return err == nil && count == 2
	}, 5*time.Second, 50*time.Millisecond, "一个 Worker 必须同时上报两个运行时")
	// 用真实调度服务立即运行已由模型工具创建的任务，Runner 必须恢复同一 Claude 会话。
	_, _, err := scheduledtasks.NewService(f.db, time.Minute, 5, 3).RunNow(ctx,
		scheduledtasks.ToolContext{SessionID: sessionID, ProjectID: projectID, AgentProfileID: profileID}, scheduleID)
	require.NoError(t, err)
	app.Runner.NotifyControlWake([]string{"claim"})
	awaitControlRunCount(t, ctx, f, runtimeidentity.Claude, 2)
	require.True(t, followupSeen.Load(), "定时任务必须到达 Claude 的真实模型上下文")
	var recordedThread string
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT external_thread_id FROM codex_thread_controls WHERE session_id=$1`, sessionID).Scan(&recordedThread))
	require.Equal(t, threads[runtimeidentity.Claude], recordedThread)
	var codexRuns int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs r JOIN codex_thread_controls c ON c.id=r.control_id WHERE c.worker_id=$1 AND c.engine='codex'`, f.workerID).Scan(&codexRuns))
	require.Equal(t, 1, codexRuns, "Claude 定时任务不能派发至 Codex")
	approvals.run(t, ctx, app, f, threads[runtimeidentity.Claude], discord)
	var claudeRuns int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs r
		JOIN codex_thread_controls c ON c.id=r.control_id WHERE c.worker_id=$1 AND c.engine='claude-code' AND r.status='completed'`, f.workerID).Scan(&claudeRuns))
	require.Equal(t, 5, claudeRuns)
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs r
		JOIN codex_thread_controls c ON c.id=r.control_id WHERE c.worker_id=$1 AND c.engine='codex'`, f.workerID).Scan(&codexRuns))
	require.Equal(t, 1, codexRuns, "Claude 审批不得产生 Codex 回合")
	automation.run(t, ctx, app, stopWorker, startWorker, f, threads[runtimeidentity.Claude], sessionID, scheduleID)
	mu.Lock()
	defer mu.Unlock()
	for engine, payloads := range requests {
		saveBootstrapArtifact(t, "models", engine, map[string]any{"requests": payloads})
	}
	saveBootstrapArtifact(t, "effects", runtimeidentity.Claude, map[string]any{"scheduleEngine": scheduleEngine,
		"threadPreserved": recordedThread == threads[runtimeidentity.Claude], "claudeCompletedRuns": claudeRuns, "codexRuns": codexRuns})
	// 工具只能使用临时 HOME；不依赖个人安装或凭据。
	require.True(t, strings.HasPrefix(f.cfg.WorkerHome, os.TempDir()) || strings.HasPrefix(f.cfg.WorkerHome, "/tmp/"))
	require.FileExists(t, filepath.Join(f.cfg.ClaudeConfigDir(), "settings.json"))
}
