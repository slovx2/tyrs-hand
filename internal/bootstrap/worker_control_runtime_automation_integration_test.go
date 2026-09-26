//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const controlAutomationMarker = "CONTROL_AUTOMATION_LIFECYCLE"

type controlAutomationCall struct {
	id               string
	args             map[string]any
	sent, resultSeen bool
	result           json.RawMessage
	scheduled        chan struct{}
	replyReady       chan struct{}
	releaseReply     chan struct{}
}

type controlAutomationScenario struct {
	mu                             sync.Mutex
	active                         *controlAutomationCall
	path, failedRun, successfulRun string
	bashSent, bashResultSeen       bool
	failureRequests                int
}

type controlAutomationPayload struct {
	Tools    []struct{ Name, Description string }
	Messages []struct {
		Role    string
		Content json.RawMessage
	}
}

// 仅脚本化模型响应，任务创建、派发和 Bash 均由真实协议及 CLI 执行。
func (s *controlAutomationScenario) respond(t *testing.T, w http.ResponseWriter, body []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	var payload controlAutomationPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Error(err)
		return false
	}
	input := automationLatestInput(payload)
	if active := s.active; active != nil {
		if active.scheduled != nil && strings.LastIndex(input, "<scheduled_task>") > strings.LastIndex(input, active.id) {
			t.Log("真实 run_now 模型请求在前一工具回合清理前到达")
			close(active.scheduled)
			active.scheduled = nil
		}
	}
	// SDK 会合并连续 user 输入，较新的调度块优先于历史中的工具标记。
	if active := s.active; active != nil && strings.LastIndex(input, active.id) > strings.LastIndex(input, "<scheduled_task>") {
		if !active.sent {
			for _, tool := range payload.Tools {
				if strings.Contains(tool.Description, "[tyrs_hand.automation_update]") {
					active.sent = true
					automationToolResponse(w, active.id, tool.Name, active.args)
					return true
				}
			}
			t.Error("真实 Claude SDK 未声明平台调度工具")
		} else if result, found, failed := automationToolResult(payload, active.id); found {
			active.resultSeen = !failed
			active.result = result
			if failed {
				t.Errorf("调度生命周期工具 %s 返回错误", active.id)
			}
			if active.replyReady != nil {
				ready, release := active.replyReady, active.releaseReply
				active.replyReady = nil
				close(ready)
				// 保持真实模型 HTTP 响应未结束，让 Worker 确实处于原工具回合内。
				// 等待时释放场景锁，避免测试栅栏自己阻止调度模型请求。
				s.mu.Unlock()
				<-release
				s.mu.Lock()
			}
		}
		bootstrapModelText(w, true)
		return true
	}
	if !strings.Contains(input, "<scheduled_task>") || !strings.Contains(input, controlAutomationMarker) {
		return false
	}
	currentTask := input[strings.LastIndex(input, "<scheduled_task>"):]
	currentTask, _, _ = strings.Cut(currentTask, "</scheduled_task>")
	assert.Contains(t, currentTask, controlAutomationMarker+"_UPDATED", "修改后的指令必须进入当前调度回合")
	assert.NotContains(t, currentTask, controlAutomationMarker+"_ORIGINAL", "当前调度任务不能继续使用旧指令；历史允许保留旧内容")
	runID := automationXMLValue(input, "run_id")
	t.Logf("调度模型请求：run_id=%s，同请求调度标记数=%d", runID, strings.Count(input, "<run_id>"))
	if runID == "" {
		t.Error("真实定时任务上下文缺少 run_id")
	}
	if !strings.Contains(string(body), "CONTROL_SSH_FIRST") {
		t.Error("定时任务恢复后丢失原始 Claude 会话历史")
	}
	if s.failedRun == "" {
		s.failedRun = runID
	}
	if s.failedRun == runID {
		s.failureRequests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"AUTOMATION_EXPECTED_TERMINAL_FAILURE"}}`))
		return true
	}
	s.successfulRun = runID
	if !s.bashSent {
		s.bashSent = true
		quotedPath := "'" + strings.ReplaceAll(s.path, "'", "'\"'\"'") + "'"
		automationToolResponse(w, "toolu_automation_bash", "Bash", map[string]any{"command": "printf 'AUTOMATION_EXECUTED\n' >> " + quotedPath})
		return true
	}
	if _, found, failed := automationToolResult(payload, "toolu_automation_bash"); found {
		s.bashResultSeen = !failed
		if failed {
			t.Error("定时任务真实 Bash 执行失败")
		}
	}
	bootstrapModelText(w, true)
	return true
}

func automationLatestInput(payload controlAutomationPayload) string {
	for i := len(payload.Messages) - 1; i >= 0; i-- {
		message := payload.Messages[i]
		if message.Role != "user" {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			return text
		}
		var blocks []struct{ Type, Text string }
		if json.Unmarshal(message.Content, &blocks) == nil {
			for _, block := range blocks {
				if block.Type == "text" {
					text += block.Text
				}
			}
			if text != "" {
				return text
			}
		}
	}
	return ""
}

func automationToolResult(payload controlAutomationPayload, id string) (json.RawMessage, bool, bool) {
	for _, message := range payload.Messages {
		var blocks []struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type == "tool_result" && block.ToolUseID == id {
				return block.Content, true, block.IsError
			}
		}
	}
	return nil, false, false
}

func automationToolResponse(w http.ResponseWriter, id, name string, input map[string]any) {
	args, _ := json.Marshal(input)
	bootstrapClaudeStart(w)
	bootstrapEvent(w, "content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}})
	bootstrapEvent(w, "content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
	bootstrapClaudeEnd(w, "tool_use")
}

func automationXMLValue(input, name string) string {
	// SDK 可合并连续 user 消息，采用最后一个调度标记代表当前输入。
	start := strings.LastIndex(input, "<"+name+">")
	if start < 0 {
		return ""
	}
	after := input[start+len(name)+2:]
	value, _, _ := strings.Cut(after, "</"+name+">")
	return strings.TrimSpace(value)
}

func (s *controlAutomationScenario) run(t *testing.T, ctx context.Context, app *WorkerApp, stopWorker func(),
	startWorker func() (*WorkerApp, func()), f controlRuntimeFixture, thread string, sessionID, previousTaskID uuid.UUID) {
	t.Helper()
	s.path = filepath.Join(f.cfg.WorkerWorkspaceRoot, "automation-lifecycle.txt")
	connect := func(worker *WorkerApp) (*codex.SocketClient, *codex.EventSubscription) {
		entry, err := worker.Runtimes.Entry(runtimeidentity.Claude)
		require.NoError(t, err)
		client, _ := connectBootstrapSSH(t, ctx, entry, f.signer)
		require.NoError(t, client.Call(ctx, "thread/resume", map[string]any{"threadId": thread}, nil))
		events := client.Subscribe(codex.ThreadFilter{ThreadID: thread})
		t.Cleanup(events.Close)
		return client, events
	}
	client, events := connect(app)
	call := func(action string, args map[string]any) map[string]any {
		active := &controlAutomationCall{id: "toolu_automation_" + action, args: args}
		var scheduled <-chan struct{}
		var replyReady <-chan struct{}
		var releaseReply func()
		if action == "retry" {
			active.scheduled = make(chan struct{})
			scheduled = active.scheduled
			active.replyReady, active.releaseReply = make(chan struct{}), make(chan struct{})
			replyReady = active.replyReady
			var once sync.Once
			releaseReply = func() { once.Do(func() { close(active.releaseReply) }) }
			t.Cleanup(releaseReply)
		}
		s.mu.Lock()
		s.active = active
		s.mu.Unlock()
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread,
			"approvalPolicy": "never", "sandboxPolicy": map[string]any{"type": "dangerFullAccess"},
			"input": []map[string]any{{"type": "text", "text": active.id, "text_elements": []any{}}}}, nil))
		if replyReady != nil {
			select {
			case <-replyReady:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			verifyAutomationWaitsForIdle(t, ctx, f, thread)
			releaseReply()
		}
		awaitBootstrapTurn(t, ctx, events)
		require.Eventually(t, func() bool {
			var completed int
			err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM tool_calls tool
				JOIN codex_turn_runs run ON run.id=tool.run_id
				WHERE tool.thread_id=$1 AND tool.call_id=$2 AND run.status='completed'`, thread, active.id).Scan(&completed)
			return err == nil && completed == 1
		}, 10*time.Second, 50*time.Millisecond, "本地终态须同步至 Control 后才可开始下一阶段")
		if scheduled != nil {
			// 确定性覆盖 run_now 已开始，但前一个工具回合的 fixture 尚未清理。
			deadline := time.NewTimer(15 * time.Second)
			defer deadline.Stop()
			select {
			case <-scheduled:
			case <-deadline.C:
				logAutomationRetryState(t, ctx, f, thread)
				t.Fatal("手动重试未在原工具回合结束后进入真实模型")
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		s.mu.Lock()
		seen, result := active.resultSeen, append(json.RawMessage(nil), active.result...)
		s.active = nil
		s.mu.Unlock()
		require.True(t, seen, "真实调度工具结果必须回到模型上下文：%s", action)
		return automationResultObject(t, result)
	}
	// 同会话只允许一个活跃心跳任务，使用真实工具结束此前的专项任务。
	deleted := call("delete_previous", map[string]any{"action": "delete", "task_id": previousTaskID.String()})
	require.Equal(t, "deleted", deleted["status"])
	originalDue := time.Now().UTC().Add(8 * time.Second).Truncate(time.Second)
	schedule := func(at time.Time) string { return "DTSTART:" + at.Format("20060102T150405Z") }
	created := call("create", map[string]any{"action": "create", "kind": "heartbeat",
		"name": controlAutomationMarker, "prompt": controlAutomationMarker + "_ORIGINAL", "schedule": schedule(originalDue)})
	taskID, err := uuid.Parse(fmt.Sprint(created["id"]))
	require.NoError(t, err)
	require.Equal(t, string(runtimeidentity.Claude), created["engine"])
	require.Equal(t, sessionID.String(), created["targetSessionId"])
	paused := call("pause", map[string]any{"action": "update", "task_id": taskID.String(),
		"name": controlAutomationMarker + "_EDITED", "prompt": controlAutomationMarker + "_UPDATED", "status": "paused"})
	require.Equal(t, "paused", paused["status"])
	require.Equal(t, controlAutomationMarker+"_EDITED", paused["name"])
	require.Equal(t, controlAutomationMarker+"_UPDATED", paused["prompt"])
	require.Nil(t, paused["nextRunAt"])
	// 跨过原始到期时间，必须既无任务记录也无文件副作用。
	timer := time.NewTimer(time.Until(originalDue.Add(time.Second)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	var runCount int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_task_runs WHERE scheduled_task_id=$1`, taskID).Scan(&runCount))
	require.Zero(t, runCount, "暂停任务不得在原始到期时间执行")
	require.NoFileExists(t, s.path)
	recoveryDue := time.Now().UTC().Add(8 * time.Second).Truncate(time.Second)
	resumed := call("resume", map[string]any{"action": "update", "task_id": taskID.String(),
		"status": "active", "schedule": schedule(recoveryDue)})
	require.Equal(t, "active", resumed["status"])
	require.Equal(t, float64(3), resumed["scheduleRevision"])
	workerID := app.Runner.WorkerID()
	stopWorker()
	// Worker 真正离线跨过到期时间，恢复后必须自动补领且不能重复物化。
	recoveryTimer := time.NewTimer(time.Until(recoveryDue.Add(time.Second)))
	defer recoveryTimer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-recoveryTimer.C:
	}
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_task_runs WHERE scheduled_task_id=$1`, taskID).Scan(&runCount))
	require.Zero(t, runCount, "Worker 离线时不能伪造已执行的任务")
	require.NoFileExists(t, s.path)
	app, _ = startWorker()
	require.Equal(t, workerID, app.Runner.WorkerID())
	// 必须由真实时钟自然到期；不改数据库时间，也不直接调用 RunNow。
	var failedID uuid.UUID
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		var taskState, taskError, blockedUntil, availability, runState, runID string
		err := f.db.QueryRowContext(ctx, `SELECT task.status,COALESCE(task.last_error_code,''),
			COALESCE(task.blocked_until::text,''),project.availability_status,COALESCE(run.status,''),COALESCE(run.id::text,'')
			FROM scheduled_tasks task JOIN workspace_projects project ON project.id=task.workspace_project_id
			LEFT JOIN scheduled_task_runs run ON run.scheduled_task_id=task.id WHERE task.id=$1
			ORDER BY run.created_at LIMIT 1`, taskID).Scan(&taskState, &taskError, &blockedUntil, &availability, &runState, &runID)
		assert.NoError(collect, err)
		assert.Equal(collect, "failed", runState, "任务状态=%s，错误=%s，阻塞截至=%s，项目=%s", taskState, taskError, blockedUntil, availability)
		if runState == "failed" {
			failedID, err = uuid.Parse(runID)
			assert.NoError(collect, err)
		}
	}, 35*time.Second, 100*time.Millisecond, "重启后自然到期任务必须进入真实模型失败终态")
	require.NoFileExists(t, s.path, "模型失败前不能伪造工具副作用")
	var taskStatus string
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT status FROM scheduled_tasks WHERE id=$1`, taskID).Scan(&taskStatus))
	require.Equal(t, "completed", taskStatus, "一次性任务到期后完成调度，不隐式自动重试模型失败")
	client, events = connect(app)
	retried := call("retry", map[string]any{"action": "run_now", "task_id": taskID.String()})
	require.Equal(t, false, retried["deduplicated"])
	run, ok := retried["run"].(map[string]any)
	require.True(t, ok)
	retryID, err := uuid.Parse(fmt.Sprint(run["id"]))
	require.NoError(t, err)
	require.NotEqual(t, failedID, retryID, "模型终态失败后的显式 run_now 创建新的手动重试")
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		var status, errorCode, errorMessage string
		err := f.db.QueryRowContext(ctx, `SELECT status,COALESCE(error_code,''),COALESCE(error_message,'')
			FROM scheduled_task_runs WHERE id=$1`, retryID).Scan(&status, &errorCode, &errorMessage)
		assert.NoError(collect, err)
		s.mu.Lock()
		assert.Equal(collect, "succeeded", status, "调度错误=%s/%s，模型Run=%s，Bash已发出=%t，结果已返回=%t",
			errorCode, errorMessage, s.successfulRun, s.bashSent, s.bashResultSeen)
		s.mu.Unlock()
	}, 30*time.Second, 100*time.Millisecond, "模型发起的真实手动重试必须完成")
	s.mu.Lock()
	bashSent, bashResultSeen := s.bashSent, s.bashResultSeen
	s.mu.Unlock()
	require.True(t, bashSent, "手动重试必须由模型发起真实 Bash")
	require.True(t, bashResultSeen, "真实 Bash 成功结果必须回到模型，不能只检查调度状态")
	content, err := os.ReadFile(s.path)
	require.NoError(t, err)
	require.Equal(t, "AUTOMATION_EXECUTED\n", string(content), "真实 Bash 只能追加一次")
	var target, external, sessionEngine, controlEngine, taskEngine, runtimeEngine, trigger string
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT run.session_id::text,control.external_thread_id,session.engine,control.engine,
		run.task_snapshot->'task'->>'engine',run.task_snapshot->'runtime'->>'engine',run.trigger FROM scheduled_task_runs run
		JOIN workspace_sessions session ON session.id=run.session_id
		JOIN codex_thread_controls control ON control.session_id=session.id WHERE run.id=$1`, retryID).
		Scan(&target, &external, &sessionEngine, &controlEngine, &taskEngine, &runtimeEngine, &trigger))
	require.Equal(t, sessionID.String(), target)
	require.Equal(t, thread, external)
	for _, engine := range []string{sessionEngine, controlEngine, taskEngine, runtimeEngine} {
		require.Equal(t, string(runtimeidentity.Claude), engine)
	}
	require.Equal(t, "run_now", trigger)
	var retryTurn, toolTurn, resolvedAction string
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT i.confirmed_codex_turn_id,i.resolved_action,tool.turn_id
		FROM scheduled_task_runs r JOIN codex_turn_intents i ON i.id=r.intent_id
		JOIN tool_calls tool ON tool.thread_id=$2 AND tool.call_id='toolu_automation_retry'
		WHERE r.id=$1`, retryID, thread).Scan(&retryTurn, &resolvedAction, &toolTurn))
	require.Equal(t, "start", resolvedAction, "等待空闲的重试必须实际启动新回合")
	require.NotEmpty(t, retryTurn)
	require.NotEqual(t, toolTurn, retryTurn, "调度执行不得与发起 run_now 的工具回合合并")
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_task_runs WHERE scheduled_task_id=$1`, taskID).Scan(&runCount))
	require.Equal(t, 2, runCount, "一个自然失败和一个手动重试，不能重复物化")
	var codexRuns int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs run
		JOIN codex_thread_controls control ON control.id=run.control_id WHERE control.worker_id=$1 AND control.engine='codex'`, f.workerID).Scan(&codexRuns))
	require.Equal(t, 1, codexRuns)
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, failedID.String(), s.failedRun)
	require.Equal(t, retryID.String(), s.successfulRun)
	require.True(t, s.bashResultSeen, "原生 Bash 的结果必须回到模型上下文")
	saveBootstrapArtifact(t, "automation-lifecycle", runtimeidentity.Claude, map[string]any{
		"taskID": taskID, "scheduledFailedRunID": failedID, "manualRetryRunID": retryID, "engine": sessionEngine,
		"threadPreserved": external == thread, "workerIDPreserved": workerID == app.Runner.WorkerID(),
		"pausedAcrossOriginalDue": true, "workerOfflineAcrossDue": true, "naturalDueAfterRestart": true, "modelFailureRequests": s.failureRequests,
		"manualRetryCompleted": true, "nativeBashResultSeen": s.bashResultSeen, "fileContent": string(content), "taskRunCount": runCount,
		"queuedWhileOriginalTurnActive": true, "manualRetryResolvedAction": resolvedAction,
		"manualRetryNativeTurn": retryTurn, "runNowToolNativeTurn": toolTurn})
}

func automationResultObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var text string
	if json.Unmarshal(raw, &text) == nil {
		raw = json.RawMessage(text)
	}
	var blocks []struct{ Type, Text string }
	if json.Unmarshal(raw, &blocks) == nil {
		for _, block := range blocks {
			if block.Type == "text" {
				return automationResultObject(t, json.RawMessage(block.Text))
			}
		}
	}
	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result), "真实平台工具必须返回结构化 JSON")
	require.NotNil(t, result)
	return result
}
