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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/interactiveprotocol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// 审批属于实际队列回合；取消后显式启动剩余条目，不能重放已执行项。
func TestWorkerControlNativeQueueApprovalAndStartRealSSH(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	markers := []string{"QUEUE_APPROVAL_FIRST", "QUEUE_APPROVAL_CANCEL", "QUEUE_APPROVAL_START"}
	scenarios := make(map[string]*codexOutageScenario)
	for _, marker := range markers {
		scenarios[marker] = &codexOutageScenario{}
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		require.NoError(t, err)
		var request struct {
			Input []struct {
				Role    string
				Content json.RawMessage
			}
			Text struct{ Format struct{ Type string } }
		}
		require.NoError(t, json.Unmarshal(body, &request))
		marker := ""
		for _, input := range request.Input {
			if input.Role == "user" {
				for _, candidate := range markers {
					if strings.Contains(string(input.Content), candidate) {
						marker = candidate
					}
				}
			}
		}
		if request.Text.Format.Type != "json_schema" && marker != "" && scenarios[marker].respond(t, w, body) {
			return
		}
		bootstrapModelText(w, false)
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	discord := startControlDiscordFixture(t, ctx, f)
	for index, marker := range markers {
		decision := "accept"
		if index == 1 {
			decision = "cancel"
		}
		path := filepath.Join(f.cfg.WorkerWorkspaceRoot, marker+".txt")
		quotedPath := "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
		scenarios[marker].active = &controlApprovalCase{id: marker, path: path, decision: decision,
			input: map[string]any{"command": "printf '" + marker + "' >> " + quotedPath, "workdir": f.cfg.WorkerWorkspaceRoot}}
	}
	workerCtx, stop := context.WithCancel(ctx)
	app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Run(workerCtx) }()
	t.Cleanup(func() { stop(); <-done; cleanup() })
	entry, err := app.Runtimes.Entry(runtimeidentity.Codex)
	require.NoError(t, err)
	type question struct {
		request codex.ServerRequest
		answer  chan string
	}
	questions := make(chan question, 4)
	client, _ := connectBootstrapSSHWithOptions(t, ctx, entry, f.signer, codex.SocketClientOptions{
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
	require.NoError(t, client.Call(ctx, "thread/start", map[string]any{"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "untrusted", "sandbox": "danger-full-access", "historyMode": "paginated"}, &started))
	threadID := started.Thread.ID
	events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(events.Close)
	add := func(marker string) string {
		t.Helper()
		var response struct{ QueuedSubmission struct{ ID string } }
		require.NoError(t, client.Call(ctx, "thread/queue/add", map[string]any{"threadId": threadID, "clientUserMessageId": uuid.NewString(), "input": []map[string]string{{"type": "text", "text": marker}}}, &response))
		return response.QueuedSubmission.ID
	}
	add(markers[0])
	var turns []string
	var runs []uuid.UUID
	thirdID := ""
	for index, marker := range markers {
		var q question
		select {
		case q = <-questions:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		require.Equal(t, interactiveprotocol.CommandApproval, q.request.Method)
		var scope struct{ ThreadID, TurnID string }
		require.NoError(t, json.Unmarshal(q.request.Params, &scope))
		require.Equal(t, threadID, scope.ThreadID)
		turns = append(turns, scope.TurnID)
		scenario := scenarios[marker]
		require.NoFileExists(t, scenario.active.path, "真实审批前不能写文件")
		var runID uuid.UUID
		require.Eventually(t, func() bool {
			return f.db.QueryRowContext(ctx, `SELECT q.run_id FROM codex_interactive_requests q
				JOIN codex_turn_runs r ON r.id=q.run_id JOIN codex_turn_intents i ON i.id=r.primary_intent_id
				WHERE q.thread_id=$1 AND q.turn_id=$2 AND q.app_server_request_id=$3::jsonb
				AND q.status='pending' AND r.confirmed_codex_turn_id=$2 AND i.instruction LIKE '%' || $4 || '%'`,
				threadID, scope.TurnID, q.request.ID, marker).Scan(&runID) == nil
		}, 10*time.Second, 25*time.Millisecond, "审批不能归属上一队列回合")
		runs = append(runs, runID)
		if index == 0 {
			add(markers[1])
			thirdID = add(markers[2])
		}
		q.answer <- scenario.active.decision
		if index == 1 {
			awaitOutageTurnStatus(t, ctx, events, "interrupted")
			require.NoFileExists(t, scenario.active.path)
			var response struct{ Turn struct{ ID string } }
			require.NoError(t, client.Call(ctx, "thread/queue/start", map[string]any{"threadId": threadID, "queuedSubmissionId": thirdID}, &response))
			require.NotEmpty(t, response.Turn.ID)
		} else {
			watcher := channelsTurnWatcher{events: events}
			watcher.awaitCompleted(t, ctx, scope.TurnID, nil)
			content, err := os.ReadFile(scenario.active.path)
			require.NoError(t, err)
			require.Equal(t, marker, string(content), "批准后文件只能写一次")
			scenario.mu.Lock()
			seen := scenario.active.resultSeen
			scenario.mu.Unlock()
			require.True(t, seen, "真实执行结果必须回到模型")
		}
	}
	require.NotEqual(t, runs[0], runs[1])
	require.NotEqual(t, runs[1], runs[2])
	discord.deliverUntil(t, ctx, func() bool {
		var pending int
		return f.db.QueryRowContext(ctx, "SELECT count(*) FROM integration_outbox WHERE status<>'completed'").Scan(&pending) == nil && pending == 0
	})
	saveBootstrapArtifact(t, "queue-approvals", runtimeidentity.Codex, map[string]any{"threadId": threadID, "turnIds": turns, "runIds": runs, "decisions": []string{"accept", "cancel", "accept"}, "explicitStart": thirdID})
}
