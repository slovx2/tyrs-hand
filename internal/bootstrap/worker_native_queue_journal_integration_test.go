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

func TestWorkerControlNativeQueueAdmissionRecoveryRealSSH(t *testing.T) {
	verifyNativeQueueJournalRecovery(t, false)
}

func TestWorkerControlNativeQueueObservedRecoveryRealSSH(t *testing.T) {
	verifyNativeQueueJournalRecovery(t, true)
}

func verifyNativeQueueJournalRecovery(t *testing.T, observed bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var calls atomic.Int64
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
			calls.Add(1)
		}
		bootstrapModelText(w, false)
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	discord := startControlDiscordFixture(t, ctx, f)
	start := func() (*WorkerApp, func()) {
		workerCtx, stopWorker := context.WithCancel(ctx)
		app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() { done <- app.Run(workerCtx) }()
		var once sync.Once
		stop := func() { once.Do(func() { stopWorker(); <-done; cleanup() }) }
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
	require.Eventually(t, func() bool {
		var count int
		return f.db.QueryRowContext(ctx, "SELECT count(*) FROM codex_thread_controls WHERE external_thread_id=$1", threadID).Scan(&count) == nil && count == 1
	}, 10*time.Second, 20*time.Millisecond)
	// 注入真实文件系统故障：队列身份可写，但后续 Run Journal 无法创建。
	runsPath := filepath.Join(f.cfg.WorkerDataRoot, "control-state", "runs")
	require.NoError(t, os.Rename(runsPath, runsPath+".saved"))
	require.NoError(t, os.WriteFile(runsPath, []byte("测试阻断 Run Journal 目录"), 0o600))
	var restoreOnce sync.Once
	restore := func() {
		restoreOnce.Do(func() {
			require.NoError(t, os.Remove(runsPath))
			require.NoError(t, os.Rename(runsPath+".saved", runsPath))
		})
	}
	t.Cleanup(restore)
	events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(events.Close)
	clientID := uuid.NewString()
	require.NoError(t, client.Call(ctx, "thread/queue/add", map[string]any{"threadId": threadID, "clientUserMessageId": clientID, "input": []map[string]string{{"type": "text", "text": "QUEUE_JOURNAL_CRASH"}}}, nil))
	watcher := channelsTurnWatcher{events: events}
	turnID := watcher.awaitCompleted(t, ctx, "", nil)
	require.EqualValues(t, 1, calls.Load())
	stop()
	restore()
	paths, err := filepath.Glob(filepath.Join(f.cfg.WorkerDataRoot, "control-state", "queues", "*.json"))
	require.NoError(t, err)
	require.Len(t, paths, 1, "Run Journal 写入失败不能删除唯一入队身份")
	data, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	var record struct {
		Task   workerprotocol.Task `json:"task"`
		TurnID string              `json:"turnId"`
	}
	require.NoError(t, json.Unmarshal(data, &record))
	require.Equal(t, turnID, record.TurnID)
	if !observed {
		// 回退到真实入队记录的持久化边界；不更改官方 CLI 的任何历史或输出。
		var admission map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(data, &admission))
		delete(admission, "turnId")
		data, err = json.Marshal(admission)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(paths[0], data, 0o600))
	}
	_, stopRecovered := start()
	require.Eventually(t, func() bool {
		var count int
		return f.db.QueryRowContext(ctx, "SELECT count(*) FROM codex_turn_runs WHERE id=$1 AND confirmed_codex_turn_id=$2 AND status='completed'", record.Task.Claimed.RunID, turnID).Scan(&count) == nil && count == 1
	}, 15*time.Second, 30*time.Millisecond, "启动时必须只读官方历史，补齐原身份对应的结果")
	discord.deliverUntil(t, ctx, func() bool {
		var pending int
		return f.db.QueryRowContext(ctx, "SELECT count(*) FROM integration_outbox WHERE status<>'completed'").Scan(&pending) == nil && pending == 0
	})
	stopRecovered()
	require.EqualValues(t, 1, calls.Load(), "恢复不允许再次调用业务模型")
	require.NoFileExists(t, paths[0], "可靠转交 Run Journal 后才能清理入队记录")
	saveBootstrapArtifact(t, "queue-journal-recovery", runtimeidentity.Codex, map[string]any{"observedBeforeRestart": observed, "threadId": threadID, "turnId": turnID, "runId": record.Task.Claimed.RunID, "modelCalls": calls.Load()})
}
