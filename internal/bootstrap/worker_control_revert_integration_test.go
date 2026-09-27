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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// 正式 Control/PostgreSQL/Redis、Worker、SSH、官方 CLI；仅模型与 Discord 网络在本地替换。
func TestWorkerControlCodexRevertRealSSH(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	var mu sync.Mutex
	var requests []json.RawMessage
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()
		var payload struct {
			Text struct{ Format struct{ Type string } }
		}
		if json.Unmarshal(body, &payload) == nil && payload.Text.Format.Type == "json_schema" {
			bootstrapEvent(w, "response.created", map[string]any{"response": map[string]any{"id": "revert-title"}})
			bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
				"id": "title-message", "type": "message", "role": "assistant",
				"content": []map[string]string{{"type": "output_text", "text": `{"title":"回退验收"}`}},
			}})
			bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": "revert-title"}})
			return
		}
		bootstrapModelText(w, false)
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
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
	watcher := channelsTurnWatcher{events: events}
	turn := func(marker string, count int) string {
		var result struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{
			"threadId": threadID, "input": []map[string]any{{"type": "text", "text": marker}},
		}, &result))
		watcher.awaitCompleted(t, ctx, result.Turn.ID, nil)
		awaitControlRunCount(t, ctx, f, runtimeidentity.Codex, count)
		return result.Turn.ID
	}
	first := turn("CONTROL_REVERT_KEEP", 1)
	var conversationID uuid.UUID
	var discordThreadID string
	discord.deliverUntil(t, ctx, func() bool {
		return f.db.QueryRowContext(ctx, `SELECT c.id,c.thread_id FROM desktop_thread_requests r
			JOIN discord_conversations c ON c.id=r.conversation_id
			WHERE r.external_thread_id=$1 AND r.status='completed'`, threadID).Scan(&conversationID, &discordThreadID) == nil
	})
	second := turn("CONTROL_REVERT_DROP", 2)
	var controlID uuid.UUID
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT id FROM codex_thread_controls
		WHERE worker_id=$1 AND external_thread_id=$2 AND engine='codex'`, f.workerID, threadID).Scan(&controlID))
	var targetID uuid.UUID
	var anchor string
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT id,COALESCE(projection_anchor,'desktop-' || id::text)
		FROM codex_turn_intents WHERE control_id=$1 AND confirmed_codex_turn_id=$2 LIMIT 1`, controlID, second).Scan(&targetID, &anchor))
	// 更早锚点不能只回退原生历史却保留错误的 Control 投影。
	require.ErrorContains(t, client.Call(ctx, "thread/revert", map[string]any{"threadId": threadID, "beforeTurnId": first}, nil), "只允许 Control 已确认的最新回合")
	var replacements int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_intents
		WHERE control_id=$1 AND operation='replace_last_turn'`, controlID).Scan(&replacements))
	require.Zero(t, replacements, "拒绝请求不能留下 replacement")
	var rolled struct {
		Thread struct{ Turns []channelsThreadTurn }
	}
	require.NoError(t, client.Call(ctx, "thread/rollback", map[string]any{"threadId": threadID, "numTurns": 1}, &rolled))
	require.Len(t, rolled.Thread.Turns, 1)
	require.Equal(t, first, rolled.Thread.Turns[0].ID)
	var reservationID, replacementTarget uuid.UUID
	var phase, replacementAnchor string
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT id,target_intent_id,replacement_phase,projection_anchor
		FROM codex_turn_intents WHERE control_id=$1 AND operation='replace_last_turn'
		ORDER BY sequence_no DESC LIMIT 1`, controlID).Scan(&reservationID, &replacementTarget, &phase, &replacementAnchor))
	require.Equal(t, targetID, replacementTarget)
	require.Equal(t, "rollback_applied", phase)
	require.Equal(t, anchor, replacementAnchor)
	// 回退已成功、替换还未提交时重启整个 Worker，验证本地 Journal 恢复。
	stopWorker()
	app, _ = startWorker()
	entry, err = app.Runtimes.Entry(runtimeidentity.Codex)
	require.NoError(t, err)
	client, _ = connectBootstrapSSH(t, ctx, entry, f.signer)
	require.NoError(t, client.Call(ctx, "thread/resume", map[string]any{"threadId": threadID, "excludeTurns": true}, nil))
	events = client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(events.Close)
	watcher.events = events
	third := turn("CONTROL_REVERT_REPLACEMENT", 3)
	var confirmed string
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT confirmed_codex_turn_id,replacement_phase
		FROM codex_turn_intents WHERE id=$1`, reservationID).Scan(&confirmed, &phase))
	require.Equal(t, third, confirmed, "下一回合必须消费原 reservation")
	require.Equal(t, "terminal", phase)
	var reverted struct {
		Thread               struct{ Turns []channelsThreadTurn }
		ItemsBackwardsCursor *string
	}
	require.NoError(t, client.Call(ctx, "thread/revert", map[string]any{"threadId": threadID, "beforeTurnId": third}, &reverted))
	require.Empty(t, reverted.Thread.Turns)
	require.NotNil(t, reverted.ItemsBackwardsCursor)
	var nativeItems struct{ Data []struct{ TurnID string } }
	require.NoError(t, client.Call(ctx, "thread/items/list", map[string]any{
		"threadId": threadID, "cursor": *reverted.ItemsBackwardsCursor, "sortDirection": "desc",
	}, &nativeItems))
	require.Len(t, nativeItems.Data, 2)
	for _, item := range nativeItems.Data {
		require.Equal(t, first, item.TurnID)
	}
	fourth := turn("CONTROL_REVERT_FINAL", 4)
	var finalTarget uuid.UUID
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT target_intent_id,projection_anchor,replacement_phase
		FROM codex_turn_intents WHERE control_id=$1 AND confirmed_codex_turn_id=$2 LIMIT 1`,
		controlID, fourth).Scan(&finalTarget, &replacementAnchor, &phase))
	require.Equal(t, reservationID, finalTarget)
	require.Equal(t, anchor, replacementAnchor, "连续回退复用同一投影位置")
	require.Equal(t, "terminal", phase)
	discord.deliverUntil(t, ctx, func() bool {
		var pending int
		err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
			WHERE payload->>'channelId'=$1 AND status<>'completed'`, discordThreadID).Scan(&pending)
		return err == nil && pending == 0
	})
	mu.Lock()
	defer mu.Unlock()
	for _, marker := range []string{"CONTROL_REVERT_REPLACEMENT", "CONTROL_REVERT_FINAL"} {
		found := false
		for _, body := range requests {
			var request struct {
				Input json.RawMessage
				Tools []json.RawMessage
			}
			require.NoError(t, json.Unmarshal(body, &request))
			if len(request.Tools) == 0 || !strings.Contains(string(request.Input), marker) {
				continue
			}
			found = true
			require.Contains(t, string(request.Input), "CONTROL_REVERT_KEEP")
			require.NotContains(t, string(request.Input), "CONTROL_REVERT_DROP")
			if marker == "CONTROL_REVERT_FINAL" {
				require.NotContains(t, string(request.Input), "CONTROL_REVERT_REPLACEMENT")
			}
		}
		require.True(t, found, "替换后的输入必须到达原生模型")
	}
	saveBootstrapArtifact(t, "models", runtimeidentity.Codex, map[string]any{"requests": requests})
	saveBootstrapArtifact(t, "effects", runtimeidentity.Codex, map[string]any{
		"threadId": threadID, "controlId": controlID, "retainedTurn": first, "finalTurn": fourth,
		"projectionAnchor": replacementAnchor, "replacementPhase": phase,
	})
}
