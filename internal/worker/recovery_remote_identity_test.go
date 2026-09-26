package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRecoveredRemoteTerminalReplaysObservedIdentityBeforeCompletion(t *testing.T) {
	for _, failure := range []struct {
		action string
		status int
	}{
		{"decide", http.StatusServiceUnavailable}, {"thread", http.StatusServiceUnavailable},
		{"submission", http.StatusServiceUnavailable}, {"confirm", http.StatusServiceUnavailable},
		{"heartbeat", http.StatusServiceUnavailable}, {"confirm", http.StatusForbidden},
		{"confirm", http.StatusConflict}, {"", http.StatusOK},
	} {
		unavailable := failure.action
		t.Run("unavailable-"+unavailable+"-"+http.StatusText(failure.status), func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				action := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
				mu.Lock()
				calls = append(calls, action)
				mu.Unlock()
				require.Equal(t, "claude-code", request.Header.Get(workerprotocol.EngineHeader))
				if action == "thread" || action == "submission" || action == "confirm" {
					var payload struct {
						ThreadID     string `json:"threadId"`
						SubmissionID string `json:"submissionId"`
						TurnID       string `json:"turnId"`
					}
					require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
					switch action {
					case "thread":
						require.Equal(t, "observed-thread", payload.ThreadID)
					case "submission":
						require.Equal(t, "observed-submission", payload.SubmissionID)
					case "confirm":
						require.Equal(t, "observed-confirmation", payload.TurnID)
					}
				}
				if action == unavailable {
					http.Error(w, "identity replay rejected", failure.status)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			store, err := newJournalStore(t.TempDir())
			require.NoError(t, err)
			journal := &runJournal{Result: &codexcontrol.TurnResult{FinalAnswer: "already-executed"},
				NextSequence: 2, PendingEvents: []workerprotocol.EventInput{{Sequence: 1,
					Type: "turn/completed", Payload: json.RawMessage(`{}`)}}}
			journal.Task.Snapshot.Runtime.Engine = "claude-code"
			journal.Task.Claimed.RunID = uuid.New()
			journal.Task.Claimed.ID = uuid.New()
			journal.Task.Claimed.ExternalThreadID = "observed-thread"
			journal.Task.Claimed.SubmissionID = "observed-submission"
			journal.Task.Claimed.ConfirmedTurnID = "observed-confirmation"
			require.NoError(t, store.save(journal))
			// 重启后只能使用持久化的真实观测值，提交 ID 与确认 ID 故意不同。
			restored, err := store.loadAll()
			require.NoError(t, err)
			require.Len(t, restored, 1)
			client, err := workerprotocol.NewClient(server.URL, "test", time.Second).ForEngine("claude-code")
			require.NoError(t, err)
			executor := &runtimeExecutor{cfg: config.Config{ControlTimeout: time.Second}, journals: store,
				client: client, logger: zap.NewNop()}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			executor.deliverTerminal(ctx, restored[0], zap.NewNop())
			mu.Lock()
			defer mu.Unlock()
			if unavailable == "" {
				require.Equal(t, []string{"decide", "thread", "submission", "confirm", "heartbeat", "events", "complete"}, calls)
				require.True(t, restored[0].TerminalDelivered)
				pending, err := store.loadAll()
				require.NoError(t, err)
				require.Empty(t, pending)
				return
			}
			require.NotContains(t, calls, "events")
			require.NotContains(t, calls, "complete")
			require.Equal(t, unavailable, calls[len(calls)-1])
			require.False(t, restored[0].TerminalDelivered)
			permanent := failure.status == http.StatusForbidden || failure.status == http.StatusConflict
			require.Equal(t, permanent, restored[0].ControlAbandoned)
			if permanent {
				require.Zero(t, restored[0].ControlRetryCount, "永久拒绝不能进入长时间退避")
			}
			pending, err := store.loadAll()
			require.NoError(t, err)
			require.Len(t, pending, 1)
			require.Equal(t, permanent, pending[0].ControlAbandoned)
			require.Equal(t, "observed-submission", pending[0].Task.Claimed.SubmissionID)
			require.Equal(t, "observed-confirmation", pending[0].Task.Claimed.ConfirmedTurnID)
			require.Len(t, pending[0].PendingEvents, 1)
		})
	}
}

func TestRemoteIdentityReplayDoesNotInferConfirmation(t *testing.T) {
	for _, test := range []struct {
		name, submission string
		result           *codexcontrol.TurnResult
	}{
		{name: "failed-before-native-turn"},
		{name: "only-submission-observed", submission: "only-submission-observed"},
		{name: "terminal-result-is-not-confirmation", submission: "only-submission-observed",
			result: &codexcontrol.TurnResult{TurnID: "result-is-not-confirmation", FinalAnswer: "finished"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls = append(calls, request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:])
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			client, err := workerprotocol.NewClient(server.URL, "test", time.Second).ForEngine("claude-code")
			require.NoError(t, err)
			journal := &runJournal{Result: test.result}
			if test.result == nil {
				journal.Failure = "turn has not started"
			}
			journal.Task.Claimed.ID = uuid.New()
			journal.Task.Claimed.RunID = uuid.New()
			journal.Task.Claimed.SubmissionID = test.submission
			executor := &runtimeExecutor{cfg: config.Config{ControlTimeout: time.Second}, client: client}
			require.NoError(t, executor.syncRunState(t.Context(), journal, nil, zap.NewNop()))
			expected := []string{"decide", "heartbeat"}
			if test.submission != "" {
				expected = []string{"decide", "submission", "heartbeat"}
			}
			require.Equal(t, expected, calls)
		})
	}
}
