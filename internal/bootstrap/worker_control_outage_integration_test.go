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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/interactiveprotocol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

// 真实 Control 短网络分区；不冒称 Worker 进程重启或全部双引擎故障验收。
func TestWorkerControlClaudeNetworkOutageRealSSH(t *testing.T) {
	requireControlNetworkIsolation(t)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	scenario := newControlApprovalScenario()
	var modelCalls atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "count_tokens") {
			_, _ = io.WriteString(w, `{"input_tokens":10}`)
			return
		}
		if req.URL.Path != "/v1/messages" {
			http.NotFound(w, req)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, 8<<20))
		if err != nil {
			t.Error(err)
			return
		}
		modelCalls.Add(1)
		if !scenario.respond(t, w, body) {
			bootstrapModelText(w, true)
		}
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	controlURL := f.cfg.WorkerControlURL
	gate := newControlTransportGate(t, f.cfg.WorkerControlURL)
	f.cfg.WorkerControlURL = gate.URL()
	f.cfg.ControlTimeout = 2 * time.Second
	workerCtx, stopWorker := context.WithCancel(ctx)
	app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Run(workerCtx) }()
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { stopWorker(); <-done; cleanup() }) })
	entry, err := app.Runtimes.Entry(runtimeidentity.Claude)
	require.NoError(t, err)
	type question struct {
		request codex.ServerRequest
		answer  chan string
	}
	questions := make(chan question, 4)
	client, _, _ := connectBootstrapSSHWithTrace(t, ctx, entry, f.signer, codex.SocketClientOptions{
		ServerRequestHandler: func(ctx context.Context, request codex.ServerRequest) (any, error) {
			q := question{request: request, answer: make(chan string, 1)}
			select {
			case questions <- q:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			select {
			case decision := <-q.answer:
				return map[string]string{"decision": decision}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	var started struct{ Thread struct{ ID string } }
	require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
		"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "on-request", "sandbox": "danger-full-access",
	}, &started))
	thread := started.Thread.ID
	events := client.Subscribe(codex.ThreadFilter{ThreadID: thread})
	t.Cleanup(events.Close)
	start := func(prompt string) {
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{
			"threadId": thread, "approvalPolicy": "on-request",
			"sandboxPolicy": map[string]any{"type": "dangerFullAccess"},
			"input":         []map[string]any{{"type": "text", "text": prompt, "text_elements": []any{}}},
		}, nil))
	}
	start("CONTROL_OUTAGE_WARMUP")
	awaitBootstrapTurn(t, ctx, events)
	awaitControlRunCount(t, ctx, f, runtimeidentity.Claude, 1)
	for index, test := range []struct{ registered, allow, conflict bool }{
		{true, true, false}, {true, false, false}, {false, true, false}, {false, false, false}, {true, true, true},
	} {
		id := fmt.Sprintf("control_outage_%d", index)
		t.Logf("Control 网络故障窗口 %s：交互先登记=%t，允许=%t", id, test.registered, test.allow)
		path := filepath.Join(f.cfg.WorkerWorkspaceRoot, id+".txt")
		active := &controlApprovalCase{tool: "Write", method: interactiveprotocol.FileApproval,
			decision: "decline", id: id, path: path, input: map[string]any{"file_path": path, "content": id + "\n"}}
		if test.allow {
			active.tool, active.method, active.decision = "Bash", interactiveprotocol.CommandApproval, "accept"
			quotedPath := "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
			active.input = map[string]any{"command": "printf '" + id + "\\n' >> " + quotedPath}
		}
		scenario.mu.Lock()
		scenario.active = active
		scenario.mu.Unlock()
		if !test.registered {
			gate.Offline()
		}
		start(id)
		var q question
		select {
		case q = <-questions:
		case <-ctx.Done():
			t.Fatal("未收到真实 SSH 审批")
		}
		require.Equal(t, active.method, q.request.Method)
		require.NoFileExists(t, path)
		t.Cleanup(func() {
			diagnosticCtx, cancelDiagnostic := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancelDiagnostic()
			var state struct{ Status, Decision, Surface, RunStatus string }
			err := f.db.QueryRowContext(diagnosticCtx, `SELECT q.status, COALESCE(q.answer->>'decision',''),
				COALESCE(q.answer_surface,''), r.status FROM codex_interactive_requests q
				JOIN codex_thread_controls c ON c.id=q.control_id JOIN codex_turn_runs r ON r.id=q.run_id
				WHERE c.worker_id=$1 AND c.engine=$2 AND q.thread_id=$3 AND q.app_server_request_id=$4::jsonb`,
				f.workerID, runtimeidentity.Claude, thread, q.request.ID).Scan(&state.Status, &state.Decision, &state.Surface, &state.RunStatus)
			if err != nil {
				t.Logf("%s：恢复后 Control 状态诊断失败：%v", id, err)
				return
			}
			t.Logf("%s：恢复后 Control 交互=%s，答案=%s，来源=%s，run=%s", id, state.Status, state.Decision, state.Surface, state.RunStatus)
		})
		if test.registered {
			require.Eventually(t, func() bool {
				var count int
				err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_interactive_requests q
					JOIN codex_thread_controls c ON c.id=q.control_id WHERE c.worker_id=$1
					AND c.engine=$2 AND q.thread_id=$3 AND q.app_server_request_id=$4::jsonb AND q.status='pending'`,
					f.workerID, runtimeidentity.Claude, thread, q.request.ID).Scan(&count)
				return err == nil && count == 1
			}, 10*time.Second, 50*time.Millisecond)
			gate.Offline()
		}
		if test.conflict {
			credential, err := os.ReadFile(f.cfg.WorkerCredentialFile)
			require.NoError(t, err)
			control, err := workerprotocol.NewClient(controlURL, string(credential), 5*time.Second).ForEngine(runtimeidentity.Claude)
			require.NoError(t, err)
			remote := workerprotocol.InteractiveAnswerRequest{RequestID: q.request.ID,
				AppServerGeneration: entry.Runtime.Generation(), WorkspaceID: f.workspaceID,
				Surface: "desktop", Answer: json.RawMessage(`{"decision":"decline"}`)}
			var scope struct{ ThreadID, TurnID, ItemID string }
			require.NoError(t, json.Unmarshal(q.request.Params, &scope))
			remote.ThreadID, remote.TurnID, remote.ItemID = scope.ThreadID, scope.TurnID, scope.ItemID
			winner, err := control.AnswerInteractive(ctx, remote)
			require.NoError(t, err)
			require.True(t, winner.Accepted)
			require.JSONEq(t, `{"decision":"decline"}`, string(winner.Answer))
		}
		q.answer <- active.decision
		awaitBootstrapTurn(t, ctx, events)
		scenario.mu.Lock()
		resultSeen := active.resultSeen
		scenario.mu.Unlock()
		require.True(t, resultSeen, "离线答案必须真实进入 SDK/CLI 的工具结果")
		assertEffect := func() {
			if !test.allow {
				require.NoFileExists(t, path)
				return
			}
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, id+"\n", string(content), "真实追加只能执行一次")
		}
		assertEffect()
		t.Logf("%s：离线原生结果与真实文件副作用已验证，开始恢复 Control", id)
		calls := modelCalls.Load()
		require.NoError(t, gate.Online())
		// SSH 独立于 Control：冲突也以本地真实结果完成，另以事件记录冲突。
		awaitControlRunCount(t, ctx, f, runtimeidentity.Claude, index+2)
		if test.conflict {
			verifyOfflineApprovalConflict(t, ctx, f, q.request)
		}
		var stored json.RawMessage
		require.Eventually(t, func() bool {
			err := f.db.QueryRowContext(ctx, `SELECT q.answer FROM codex_interactive_requests q
				JOIN codex_thread_controls c ON c.id=q.control_id WHERE c.worker_id=$1 AND c.engine=$2
				AND q.thread_id=$3 AND q.app_server_request_id=$4::jsonb AND q.status='resolved'`,
				f.workerID, runtimeidentity.Claude, thread, q.request.ID).Scan(&stored)
			return err == nil
		}, 12*time.Second, 50*time.Millisecond, "恢复后真实 Control 必须补记离线答案")
		var answer struct{ Decision string }
		require.NoError(t, json.Unmarshal(stored, &answer))
		wantDecision := active.decision
		if test.conflict {
			wantDecision = "decline"
		}
		require.Equal(t, wantDecision, answer.Decision)
		require.Equal(t, calls, modelCalls.Load(), "网络恢复不能重新请求模型")
		assertEffect()
	}
}

func verifyOfflineApprovalConflict(t *testing.T, ctx context.Context, f controlRuntimeFixture, request codex.ServerRequest) {
	t.Helper()
	var runID string
	var result json.RawMessage
	require.Eventually(t, func() bool {
		return f.db.QueryRowContext(ctx, `SELECT r.id::text, i.result FROM codex_turn_runs r
			JOIN codex_turn_intents i ON i.id=r.primary_intent_id
			JOIN codex_interactive_requests q ON q.run_id=r.id
			WHERE r.worker_id=$1 AND q.app_server_request_id=$2::jsonb AND r.status='completed'`,
			f.workerID, request.ID).Scan(&runID, &result) == nil
	}, 12*time.Second, 50*time.Millisecond, "冲突不能把 SSH 已真实执行的 Run 改判为失败")
	require.NotEmpty(t, result, "Control 必须收到本地真实结果")
	var events []json.RawMessage
	require.Eventually(t, func() bool {
		rows, err := f.db.QueryContext(ctx, `SELECT payload FROM agent_events
			WHERE run_id=$1 AND event_type='interactive.offline_conflict'`, runID)
		if err != nil {
			return false
		}
		defer func() { _ = rows.Close() }()
		events = events[:0]
		for rows.Next() {
			var payload json.RawMessage
			if rows.Scan(&payload) != nil {
				return false
			}
			events = append(events, payload)
		}
		return rows.Err() == nil && len(events) > 0
	}, 12*time.Second, 50*time.Millisecond, "Control 必须收到离线审批冲突事件")
	require.Len(t, events, 1, "冲突事件恰好一次")
	var conflict struct {
		Conflict    string          `json:"conflict"`
		LocalAnswer json.RawMessage `json:"localAnswer"`
		Remote      struct {
			Answer json.RawMessage `json:"answer"`
		} `json:"remote"`
	}
	require.NoError(t, json.Unmarshal(events[0], &conflict))
	require.Equal(t, "control_answer_mismatch", conflict.Conflict)
	require.JSONEq(t, `{"decision":"accept"}`, string(conflict.LocalAnswer))
	require.JSONEq(t, `{"decision":"decline"}`, string(conflict.Remote.Answer))
	path := filepath.Join(f.cfg.ClaudeStateDir(), "control-state", "runs", runID+".json")
	require.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return os.IsNotExist(err)
	}, 5*time.Second, 25*time.Millisecond, "终态送达后必须清理 Journal")
	saveBootstrapArtifact(t, "offline-conflict", runtimeidentity.Claude, map[string]any{
		"runStatus": "completed", "conflictEvent": "interactive.offline_conflict", "remoteDecision": "decline",
		"localDecision": "accept", "localResultDelivered": true, "sideEffectNotReplayed": true,
	})
}
