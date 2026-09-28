//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// 连续原生回合各自创建 Control Run，平台工具必须使用对应回合的身份。
func TestWorkerControlNativeQueueLifecycleRealSSH(t *testing.T) {
	verifyNativeQueueLifecycle(t, runtimeidentity.Codex)
}

func TestWorkerControlClaudeQueueLifecycleRealSSH(t *testing.T) {
	verifyNativeQueueLifecycle(t, runtimeidentity.Claude)
}

func verifyNativeQueueLifecycle(t *testing.T, engine runtimeidentity.Engine) {
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce, enteredOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var toolsReturned atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != queueModelPath(engine) {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		require.NoError(t, err)
		if engine == runtimeidentity.Claude {
			claudeQueueLifecycleResponse(t, ctx, w, body, entered, release, &enteredOnce, &toolsReturned)
			return
		}
		var request struct {
			Input []struct {
				Type, Role      string
				CallID          string `json:"call_id"`
				Content, Output json.RawMessage
			}
			Text struct{ Format struct{ Type string } }
		}
		require.NoError(t, json.Unmarshal(body, &request))
		if request.Text.Format.Type == "json_schema" {
			bootstrapEvent(w, "response.created", map[string]any{"response": map[string]string{"id": "queue-title"}})
			bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{"type": "message", "role": "assistant", "id": "queue-title-message", "content": []map[string]string{{"type": "output_text", "text": `{"title":"原生队列"}`}}}})
			bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]string{"id": "queue-title"}})
			return
		}
		marker := ""
		for _, input := range request.Input {
			if input.Role == "user" {
				for _, candidate := range []string{"QUEUE_FIRST", "QUEUE_SECOND", "QUEUE_SECOND_EDITED", "QUEUE_DELETED"} {
					if strings.Contains(string(input.Content), candidate) {
						marker = candidate
					}
				}
			}
		}
		require.NotEmpty(t, marker)
		require.NotEqual(t, "QUEUE_DELETED", marker, "删除的原生条目不能进入模型")
		callID := "native-" + marker
		for _, input := range request.Input {
			if input.Type == "function_call_output" && input.CallID == callID {
				require.Contains(t, string(input.Output), "QUEUE_FIRST", "平台工具应返回真实创建或查询的任务")
				toolsReturned.Add(1)
				bootstrapModelText(w, false)
				return
			}
		}
		if marker == "QUEUE_FIRST" {
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
		}
		arguments := map[string]any{"action": "create", "kind": "heartbeat", "name": marker, "prompt": marker + "_FOLLOWUP", "schedule": "DTSTART:20300102T000000Z\nRRULE:FREQ=DAILY"}
		if marker == "QUEUE_SECOND_EDITED" {
			arguments = map[string]any{"action": "list"}
		}
		args, err := json.Marshal(arguments)
		require.NoError(t, err)
		bootstrapEvent(w, "response.created", map[string]any{"response": map[string]string{"id": callID}})
		bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{"type": "function_call", "namespace": "tyrs_hand", "name": "automation_update", "call_id": callID, "arguments": string(args)}})
		bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]string{"id": callID}})
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
	entry, err := app.Runtimes.Entry(engine)
	require.NoError(t, err)
	client, _ := connectBootstrapSSH(t, ctx, entry, f.signer)
	var started struct{ Thread struct{ ID string } }
	require.NoError(t, client.Call(ctx, "thread/start", map[string]any{"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "never", "sandbox": "danger-full-access", "historyMode": "paginated"}, &started))
	threadID := started.Thread.ID
	events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(events.Close)
	add := func(marker string) string {
		t.Helper()
		var response struct{ QueuedSubmission struct{ ID string } }
		require.NoError(t, client.Call(ctx, "thread/queue/add", map[string]any{"threadId": threadID, "clientUserMessageId": uuid.NewString(), "input": []map[string]string{{"type": "text", "text": marker}}}, &response))
		require.NotEmpty(t, response.QueuedSubmission.ID)
		return response.QueuedSubmission.ID
	}
	add("QUEUE_FIRST")
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	second := add("QUEUE_SECOND")
	var updated any
	require.NoError(t, client.Call(ctx, "thread/queue/update", map[string]any{"threadId": threadID, "queuedSubmissionId": second, "input": []map[string]string{{"type": "text", "text": "QUEUE_SECOND_EDITED"}}}, &updated))
	deleted := add("QUEUE_DELETED")
	var deletion struct{ Deleted bool }
	require.NoError(t, client.Call(ctx, "thread/queue/delete", map[string]any{"threadId": threadID, "queuedSubmissionId": deleted}, &deletion))
	require.True(t, deletion.Deleted)
	releaseOnce.Do(func() { close(release) })
	watcher := channelsTurnWatcher{events: events}
	turns := []string{watcher.awaitCompleted(t, ctx, "", nil), watcher.awaitCompleted(t, ctx, "", nil)}
	require.NotEqual(t, turns[0], turns[1])
	require.EqualValues(t, 2, toolsReturned.Load())
	for index, marker := range []string{"QUEUE_FIRST", "QUEUE_SECOND_EDITED"} {
		require.Eventually(t, func() bool {
			var count int
			err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_intents intent
				JOIN codex_turn_runs run ON run.primary_intent_id=intent.id
				JOIN tool_calls tool ON tool.run_id=run.id
				WHERE intent.confirmed_codex_turn_id=$1 AND run.confirmed_codex_turn_id=$1
				AND intent.instruction LIKE '%' || $2 || '%' AND run.status='completed'
				AND tool.thread_id=$3 AND tool.call_id=$4`, turns[index], marker, threadID, "native-"+marker).Scan(&count)
			return err == nil && count == 1
		}, 20*time.Second, 100*time.Millisecond, "连续队列工具必须精确绑定各自的 Control Run")
		require.Eventually(t, func() bool {
			var count int
			err := f.db.QueryRowContext(ctx, `SELECT count(DISTINCT event.payload->'item'->>'type')
				FROM agent_events event JOIN codex_turn_runs run ON run.id=event.run_id
				WHERE run.confirmed_codex_turn_id=$1 AND event.event_type='item/completed'
				AND event.payload->'item'->>'type' IN ('userMessage','dynamicToolCall','agentMessage')`, turns[index]).Scan(&count)
			return err == nil && count == 3
		}, 10*time.Second, 50*time.Millisecond, "每回合用户输入、动态工具和最终回答事件都必须进入 Control")
	}
	var schedules int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_tasks WHERE workspace_id=$1 AND name IN ('QUEUE_FIRST','QUEUE_SECOND')`, f.workspaceID).Scan(&schedules))
	require.Equal(t, 1, schedules, "同一会话只创建一个 heartbeat，第二回合只查询")
	discord.deliverUntil(t, ctx, func() bool {
		var pending int
		err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox WHERE status<>'completed'`).Scan(&pending)
		return err == nil && pending == 0
	})
	saveBootstrapArtifact(t, "queue-control", engine, map[string]any{"threadId": threadID, "turnIds": turns, "toolResults": toolsReturned.Load(), "schedules": schedules, "deletedSubmissionId": deleted})
}
