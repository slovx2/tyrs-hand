//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
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
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWorkerControlNativeQueueWholeWorkerRestartRealSSH(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	var modelCalls atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		require.NoError(t, err)
		var request struct {
			Text struct{ Format struct{ Type string } }
		}
		require.NoError(t, json.Unmarshal(body, &request))
		if request.Text.Format.Type != "json_schema" {
			require.Contains(t, string(body), "QUEUE_RESTART_")
			modelCalls.Add(1)
		}
		bootstrapModelText(w, false)
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	discord := startControlDiscordFixture(t, ctx, f)
	start := func() (*WorkerApp, func()) {
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
	app, stop := start()
	entry, err := app.Runtimes.Entry(runtimeidentity.Codex)
	require.NoError(t, err)
	client, _ := connectBootstrapSSH(t, ctx, entry, f.signer)
	var started struct{ Thread struct{ ID string } }
	require.NoError(t, client.Call(ctx, "thread/start", map[string]any{"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "never", "sandbox": "danger-full-access", "historyMode": "paginated"}, &started))
	threadID := started.Thread.ID
	// 官方空线程没有 rollout；先完成真实回合，再测试磁盘队列的整 Worker 恢复。
	warmupEvents := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	var warmup struct{ Turn struct{ ID string } }
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": threadID, "input": []map[string]string{{"type": "text", "text": "QUEUE_RESTART_WARMUP"}}}, &warmup))
	warmupWatcher := channelsTurnWatcher{events: warmupEvents}
	warmupWatcher.awaitCompleted(t, ctx, warmup.Turn.ID, nil)
	warmupEvents.Close()
	require.EqualValues(t, 1, modelCalls.Load())
	require.Eventually(t, func() bool {
		var count int
		return f.db.QueryRowContext(ctx, "SELECT count(*) FROM codex_thread_controls WHERE external_thread_id=$1", threadID).Scan(&count) == nil && count == 1
	}, 10*time.Second, 20*time.Millisecond)
	// 卸载原生线程后入队，确保 Worker 停止前条目仍在磁盘队列，尚未调用模型。
	require.NoError(t, entry.Runtime.Restart())
	client, _ = connectBootstrapSSH(t, ctx, entry, f.signer)
	var queuedIDs []string
	for _, marker := range []string{"QUEUE_RESTART_FIRST", "QUEUE_RESTART_SECOND"} {
		var response struct{ QueuedSubmission struct{ ID string } }
		require.NoError(t, client.Call(ctx, "thread/queue/add", map[string]any{"threadId": threadID, "clientUserMessageId": uuid.NewString(), "input": []map[string]string{{"type": "text", "text": marker}}}, &response))
		queuedIDs = append(queuedIDs, response.QueuedSubmission.ID)
	}
	// 排序只改变原生执行次序，两个入队 Run 的身份仍保持不变。
	require.NoError(t, client.Call(ctx, "thread/queue/reorder", map[string]any{"threadId": threadID, "queuedSubmissionIds": []string{queuedIDs[1], queuedIDs[0]}}, nil))
	require.EqualValues(t, 1, modelCalls.Load(), "卸载线程入队不能调用模型")
	admissions := map[string]uuid.UUID{}
	require.NoError(t, filepath.WalkDir(f.cfg.WorkerDataRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(filepath.Dir(path)) != "queues" || !strings.HasSuffix(path, ".json") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var record struct {
			Task workerprotocol.Task `json:"task"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		for _, marker := range []string{"QUEUE_RESTART_FIRST", "QUEUE_RESTART_SECOND"} {
			if strings.Contains(record.Task.Claimed.Instruction, marker) {
				admissions[marker] = record.Task.Claimed.RunID
			}
		}
		return nil
	}))
	require.Len(t, admissions, 2, "入队前必须持久化两个独立 Run 身份")
	stop()
	app, _ = start()
	entry, err = app.Runtimes.Entry(runtimeidentity.Codex)
	require.NoError(t, err)
	client, _ = connectBootstrapSSH(t, ctx, entry, f.signer)
	events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(events.Close)
	var resumed any
	require.NoError(t, client.Call(ctx, "thread/resume", map[string]any{"threadId": threadID}, &resumed))
	watcher := channelsTurnWatcher{events: events}
	turns := []string{watcher.awaitCompleted(t, ctx, "", nil), watcher.awaitCompleted(t, ctx, "", nil)}
	require.EqualValues(t, 3, modelCalls.Load(), "预热一次，完整 Worker 重启后队列各执行一次")
	history, err := codex.NewRuntime(client).ReadThread(ctx, threadID)
	require.NoError(t, err)
	require.Len(t, history.Turns, 3, "恢复快照必须包含 paginated 线程的完整真实回合")
	for index, marker := range []string{"QUEUE_RESTART_SECOND", "QUEUE_RESTART_FIRST"} {
		require.Eventually(t, func() bool {
			var count int
			err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs run JOIN codex_turn_intents intent ON intent.id=run.primary_intent_id
				WHERE run.id=$1 AND run.confirmed_codex_turn_id=$2 AND run.status='completed' AND intent.instruction LIKE '%' || $3 || '%'`,
				admissions[marker], turns[index], marker).Scan(&count)
			return err == nil && count == 1
		}, 15*time.Second, 50*time.Millisecond, "恢复必须沿用入队时的 Run 身份与实际输入")
	}
	discord.deliverUntil(t, ctx, func() bool {
		var pending int
		return f.db.QueryRowContext(ctx, "SELECT count(*) FROM integration_outbox WHERE status<>'completed'").Scan(&pending) == nil && pending == 0
	})
	saveBootstrapArtifact(t, "queue-worker-restart", runtimeidentity.Codex, map[string]any{"threadId": threadID, "turnIds": turns, "admissions": admissions, "modelCalls": modelCalls.Load()})
}
