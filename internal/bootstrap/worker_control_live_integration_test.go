//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// MIGRATION-004：既有 Codex Live API、真实 Provider WebSocket handoff、真实 Worker 执行和 SSH 接力。
func TestWorkerControlLiveCodexRealSSH(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	provider := newControlLiveProvider(t)
	model := &controlLiveModel{}
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { model.respond(t, w, r) }))
	t.Cleanup(modelServer.Close)
	ready := make(chan struct{})
	close(ready)
	identity := controlLiveIdentity{}
	f := newControlRuntimeFixture(t, ctx, modelServer.URL, ready, identity.configure(t, ctx, provider.server.URL))
	model.setPath(filepath.Join(f.cfg.WorkerWorkspaceRoot, "live-native-effect.txt"))
	workerCtx, stopWorker := context.WithCancel(ctx)
	app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Run(workerCtx) }()
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { stopWorker(); <-done; cleanup() }) })
	threads := map[runtimeidentity.Engine]string{}
	sessions := map[runtimeidentity.Engine]uuid.UUID{}
	clients := map[runtimeidentity.Engine]*codex.SocketClient{}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		entry, entryErr := app.Runtimes.Entry(engine)
		require.NoError(t, entryErr)
		client, _ := connectBootstrapSSH(t, ctx, entry, f.signer)
		clients[engine] = client
		var started struct{ Thread struct{ ID string } }
		require.NoError(t, client.Call(ctx, "thread/start", map[string]any{"cwd": f.cfg.WorkerWorkspaceRoot,
			"approvalPolicy": "never", "sandbox": "danger-full-access", "historyMode": "paginated"}, &started))
		threads[engine] = started.Thread.ID
		if engine == runtimeidentity.Codex {
			events := client.Subscribe(codex.ThreadFilter{ThreadID: started.Thread.ID})
			require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": started.Thread.ID,
				"input": []map[string]any{{"type": "text", "text": "LIVE_SSH_CONTEXT", "text_elements": []any{}}}}, nil))
			awaitBootstrapTurn(t, ctx, events)
			events.Close()
			awaitControlRunCount(t, ctx, f, engine, 1)
		}
		var sessionID uuid.UUID
		require.Eventually(t, func() bool {
			return f.db.QueryRowContext(ctx, `SELECT session_id FROM codex_thread_controls
				WHERE worker_id=$1 AND engine=$2 AND external_thread_id=$3`, f.workerID, engine, started.Thread.ID).Scan(&sessionID) == nil
		}, 10*time.Second, 50*time.Millisecond)
		sessions[engine] = sessionID
	}
	api := &controlLiveHTTP{base: f.cfg.WorkerControlURL, client: http.Client{Timeout: 15 * time.Second}}
	api.login(t, ctx, identity)
	var denied struct{ Detail string }
	api.request(t, ctx, http.MethodPost, "/api/v1/client/live-conversations", map[string]any{
		"workerId": f.workerID, "sessionId": sessions[runtimeidentity.Claude], "model": "live-loopback-model"}, http.StatusUnprocessableEntity, &denied)
	require.Contains(t, denied.Detail, "不支持在 Claude 会话中使用 Live 语音")
	require.Zero(t, provider.count(), "Claude 拒绝必须发生在创建 Live Provider 会话之前")
	require.Equal(t, int64(1), model.codexCalls.Load())
	require.Zero(t, model.claudeCalls.Load())
	require.Eventually(t, func() bool { return model.titleCalls.Load() == 1 }, 5*time.Second, 20*time.Millisecond,
		"标题请求必须单独记账")
	var conversation struct {
		ID, WorkspaceSessionID uuid.UUID
	}
	api.request(t, ctx, http.MethodPost, "/api/v1/client/live-conversations", map[string]any{
		"workerId": f.workerID, "sessionId": sessions[runtimeidentity.Codex], "model": "live-loopback-model", "voice": "marin"}, http.StatusCreated, &conversation)
	require.Equal(t, sessions[runtimeidentity.Codex], conversation.WorkspaceSessionID)
	var liveSession struct {
		SessionID uuid.UUID
		Transport struct{ Type, AnswerSDP string }
	}
	api.request(t, ctx, http.MethodPost, "/api/v1/client/live-conversations/"+conversation.ID.String()+"/sessions",
		map[string]string{"offerSdp": "v=0\r\nloopback-offer\r\n", "platform": "web"}, http.StatusCreated, &liveSession)
	require.Equal(t, "webrtc", liveSession.Transport.Type)
	require.Equal(t, "v=0\r\nloopback-answer\r\n", liveSession.Transport.AnswerSDP)
	select {
	case <-provider.attached:
	case <-ctx.Done():
		t.Fatal("真实 Control 没有连接 Live Provider WebSocket")
	}
	client := clients[runtimeidentity.Codex]
	events := client.Subscribe(codex.ThreadFilter{ThreadID: threads[runtimeidentity.Codex]})
	defer events.Close()
	provider.send(t, map[string]any{"type": "input_transcript.done", "id": "live-input", "item_id": "voice-item",
		"transcript": "LIVE_HANDOFF_WRITE"})
	provider.send(t, map[string]any{"type": "conversation.handoff.requested", "id": "live-handoff", "handoff_id": "handoff-one"})
	awaitBootstrapTurn(t, ctx, events)
	awaitControlRunCount(t, ctx, f, runtimeidentity.Codex, 2)
	content, err := os.ReadFile(model.pathValue())
	require.NoError(t, err)
	require.Equal(t, "LIVE_EFFECT\n", string(content), "必须实际执行一次文件写入")
	require.True(t, model.contextSeen.Load())
	require.True(t, model.resultSeen.Load())
	require.Equal(t, int64(3), model.codexCalls.Load())
	require.Zero(t, model.claudeCalls.Load())
	var history struct {
		Thread struct {
			ID    string
			Turns []struct {
				Status string
				Items  json.RawMessage
			}
		}
	}
	require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": threads[runtimeidentity.Codex], "includeTurns": true}, &history))
	require.Equal(t, threads[runtimeidentity.Codex], history.Thread.ID)
	require.Len(t, history.Thread.Turns, 2)
	for _, turn := range history.Thread.Turns {
		require.Equal(t, "completed", turn.Status)
	}
	require.Contains(t, string(history.Thread.Turns[0].Items), "LIVE_SSH_CONTEXT")
	require.Contains(t, string(history.Thread.Turns[1].Items), "LIVE_HANDOFF_WRITE")
	require.Contains(t, string(history.Thread.Turns[1].Items), "LIVE_HANDOFF_DONE")
	verifyLiveNoDuplicate(t, ctx, f, provider, model, sessions[runtimeidentity.Codex])
	api.request(t, ctx, http.MethodPost, "/api/v1/client/live-sessions/"+liveSession.SessionID.String()+"/close", nil, http.StatusOK, nil)
	select {
	case <-provider.closed:
	case <-ctx.Done():
		t.Fatal("真实 Provider 没有收到 session.close")
	}
	saveBootstrapArtifact(t, "live-effects", runtimeidentity.Codex, map[string]any{
		"caseId": "MIGRATION-004", "workerId": f.workerID, "threadId": threads[runtimeidentity.Codex],
		"workspaceSessionId": sessions[runtimeidentity.Codex], "liveSessionId": liveSession.SessionID,
		"apiCalls": api.calls, "providerSessions": provider.count(), "modelCalls": model.codexCalls.Load(),
		"titleModelCalls": model.titleCalls.Load(),
		"historyTurns":    len(history.Thread.Turns), "fileContent": string(content), "contextSeen": model.contextSeen.Load(),
		"toolResultSeen": model.resultSeen.Load(), "claudeModelCalls": model.claudeCalls.Load(), "claudeBindStatus": http.StatusUnprocessableEntity})
}

func verifyLiveNoDuplicate(t *testing.T, ctx context.Context, f controlRuntimeFixture, provider *controlLiveProvider, model *controlLiveModel, sessionID uuid.UUID) {
	t.Helper()
	provider.send(t, map[string]any{"type": "conversation.handoff.requested", "id": "live-handoff-duplicate", "handoff_id": "handoff-one"})
	// 等待真实事件被管理器记录，不能用盲等掩盖重复 handoff 尚未到达。
	require.Eventually(t, func() bool {
		var count int
		err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM live_events WHERE event_id='live-handoff-duplicate'`).Scan(&count)
		return err == nil && count == 1
	}, 5*time.Second, 20*time.Millisecond)
	var count int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_intents WHERE session_id=$1 AND input_surface='live'`, sessionID).Scan(&count))
	require.Equal(t, 1, count)
	// 已观察到重复事件入库，再覆盖一轮真实 Worker 领取周期，防止迟到的重复执行漏检。
	require.Never(t, func() bool { return model.codexCalls.Load() > 3 }, 500*time.Millisecond, 20*time.Millisecond)
	require.Equal(t, int64(3), model.codexCalls.Load())
	content, err := os.ReadFile(model.pathValue())
	require.NoError(t, err)
	require.Equal(t, "LIVE_EFFECT\n", string(content))
	var claudeRuns int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs r JOIN codex_thread_controls c ON c.id=r.control_id WHERE c.worker_id=$1 AND c.engine='claude-code'`, f.workerID).Scan(&claudeRuns))
	require.Zero(t, claudeRuns)
}
