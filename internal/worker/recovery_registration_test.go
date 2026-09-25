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

func TestRecoveredDesktopTerminalRequiresRegistrationBeforeEvents(t *testing.T) {
	for _, unavailable := range []string{"thread-prepare", "thread-complete", "desktop-turns", "submission", "confirm", ""} {
		t.Run("unavailable-"+unavailable, func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				unwrapWorkerTestRequest(t, request)
				path := request.URL.Path
				action := path[strings.LastIndex(path, "/")+1:]
				if action == "desktop-thread-requests" {
					action = "thread-prepare"
				}
				if strings.Contains(path, "/desktop-thread-requests/") {
					action = "thread-complete"
				}
				require.Equal(t, "claude-code", request.Header.Get(workerprotocol.EngineHeader))
				mu.Lock()
				calls = append(calls, action)
				mu.Unlock()
				if action == unavailable {
					http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			root := t.TempDir()
			store, err := newJournalStore(root)
			require.NoError(t, err)
			runID, intentID := uuid.New(), uuid.New()
			journal := &runJournal{Result: &codexcontrol.TurnResult{FinalAnswer: "finished-before-crash"},
				DesktopRequest: &workerprotocol.DesktopTurnPrepareRequest{
					WorkspaceID: uuid.New(), RunID: runID, IntentID: intentID, TurnID: "native-turn",
					RequestKey: strings.Repeat("a", 64), Params: json.RawMessage(`{"threadId":"thread"}`)},
				NextSequence: 2, PendingEvents: []workerprotocol.EventInput{{Sequence: 1, Type: "turn/completed", Payload: json.RawMessage(`{}`)}},
			}
			journal.Task.Snapshot.Runtime.Engine = "claude-code"
			journal.Task.Claimed.RunID = runID
			journal.Task.Claimed.ID = intentID
			journal.Task.Claimed.ConfirmedTurnID = "native-turn"
			require.NoError(t, store.save(journal))
			require.NoError(t, store.saveThread(threadRegistrationJournal{Engine: "claude-code",
				Request: workerprotocol.DesktopThreadPrepareRequest{WorkspaceID: journal.DesktopRequest.WorkspaceID,
					RequestKey: "persisted-native-thread", Operation: "start", Params: json.RawMessage(`{}`)},
				Response: json.RawMessage(`{"thread":{"id":"thread"}}`),
			}))
			// 重新打开文件模拟进程重启，不能依赖首次提交时的内存屏障。
			store, err = newJournalStore(root)
			require.NoError(t, err)
			restored, err := store.loadAll()
			require.NoError(t, err)
			require.Len(t, restored, 1)
			client, err := workerprotocol.NewClient(server.URL, "test", time.Second).ForEngine("claude-code")
			require.NoError(t, err)
			executor := &runtimeExecutor{cfg: config.Config{ControlTimeout: time.Second}, journals: store,
				client: client, logger: zap.NewNop()}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			executor.deliverTerminal(ctx, restored[0], zap.NewNop())
			mu.Lock()
			defer mu.Unlock()
			if unavailable == "" {
				require.Equal(t, []string{"thread-prepare", "thread-complete", "desktop-turns", "submission", "confirm", "events", "complete"}, calls)
				require.True(t, restored[0].TerminalDelivered)
				pendingThreads, err := store.loadThreads()
				require.NoError(t, err)
				require.Empty(t, pendingThreads)
			} else {
				require.NotContains(t, calls, "events")
				require.NotContains(t, calls, "complete")
				require.Equal(t, unavailable, calls[len(calls)-1])
				require.False(t, restored[0].TerminalDelivered)
				require.False(t, restored[0].ControlAbandoned)
				pending, err := store.loadAll()
				require.NoError(t, err)
				require.Len(t, pending, 1)
				require.Len(t, pending[0].PendingEvents, 1)
			}
		})
	}
}
