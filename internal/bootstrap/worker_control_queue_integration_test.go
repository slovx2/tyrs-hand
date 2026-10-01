//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// 空闲线程的原生 queue/add 会自动启动回合，必须投影实际输入和终态。
func TestWorkerControlCodexQueueRealSSH(t *testing.T) {
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
			Input json.RawMessage
			Text  struct{ Format struct{ Type string } }
		}
		require.NoError(t, json.Unmarshal(body, &request))
		if request.Text.Format.Type == "json_schema" {
			bootstrapEvent(w, "response.created", map[string]any{"response": map[string]any{"id": "queue-title"}})
			bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
				"id": "queue-title-message", "type": "message", "role": "assistant",
				"content": []map[string]string{{"type": "output_text", "text": `{"title":"队列验收"}`}},
			}})
			bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": "queue-title"}})
			return
		}
		calls.Add(1)
		require.Contains(t, string(request.Input), "QUEUE_AUTO")
		bootstrapModelText(w, false)
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
	client, _ := connectBootstrapSSH(t, ctx, entry, f.signer)
	var started struct{ Thread struct{ ID string } }
	require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
		"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "never",
		"sandbox": "danger-full-access", "historyMode": "paginated",
	}, &started))
	threadID := started.Thread.ID
	events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(events.Close)
	var queued struct{ QueuedSubmission struct{ ID string } }
	require.NoError(t, client.Call(ctx, "thread/queue/add", map[string]any{
		"threadId": threadID, "input": []map[string]string{{"type": "text", "text": "QUEUE_AUTO"}},
		"clientUserMessageId": uuid.NewString(),
	}, &queued))
	require.NotEmpty(t, queued.QueuedSubmission.ID)
	watcher := channelsTurnWatcher{events: events}
	turnID := watcher.awaitCompleted(t, ctx, "", nil)
	require.EqualValues(t, 1, calls.Load(), "自动队列消息只能执行一次")
	require.Eventually(t, func() bool {
		var instruction, status string
		err := f.db.QueryRowContext(ctx, `SELECT instruction,status FROM codex_turn_intents
			WHERE confirmed_codex_turn_id=$1`, turnID).Scan(&instruction, &status)
		return err == nil && strings.Contains(instruction, "QUEUE_AUTO") && status == "completed"
	}, 20*time.Second, 100*time.Millisecond, "原生队列回合必须登记实际输入并完成 Control Run")
	discord.deliverUntil(t, ctx, func() bool {
		var pending int
		err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox WHERE status<>'completed'`).Scan(&pending)
		return err == nil && pending == 0
	})
}
