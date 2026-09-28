//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

// 原始 PG 日志：交互登记持有 Control 等待 Run，事件插入持有 Run 等待 Control 外键。
func TestWorkerEventsLockParentsBeforeRun(t *testing.T) {
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		for _, parent := range []string{"session", "control", "intent"} {
			t.Run(string(engine)+"/"+parent, func(t *testing.T) {
				f := newSessionRuntimeFixture(t)
				ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
				defer cancel()
				task := f.startRun(t, engine)
				tx, err := f.db.BeginTx(ctx, nil)
				require.NoError(t, err)
				defer func() { _ = tx.Rollback() }()
				var pid int
				var locked uuid.UUID
				require.NoError(t, tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
				query, id := "SELECT id FROM workspace_sessions WHERE id=$1 FOR UPDATE", task.Claimed.SessionID
				if parent == "control" {
					query, id = "SELECT id FROM codex_thread_controls WHERE id=$1 FOR UPDATE", task.Claimed.ControlID
				}
				if parent == "intent" {
					query, id = "SELECT id FROM codex_turn_intents WHERE id=$1 FOR UPDATE", task.Claimed.ID
				}
				require.NoError(t, tx.QueryRowContext(ctx, query, id).Scan(&locked))
				done := make(chan error, 1)
				go func() {
					done <- f.clients[engine].Events(ctx, task, []workerprotocol.EventInput{{Sequence: 1, Type: "item/completed",
						Payload: json.RawMessage(`{"threadId":"shared-thread","turnId":"turn","item":{"id":"event-lock-item","type":"agentMessage","text":"事件锁序"}}`)}})
				}()
				require.Eventually(t, func() bool {
					var waiting bool
					return f.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", pid).Scan(&waiting) == nil && waiting
				}, 3*time.Second, 10*time.Millisecond, "真实事件请求必须到达父记录锁等待")
				require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM codex_turn_runs WHERE id=$1 FOR UPDATE NOWAIT", task.Claimed.RunID).Scan(&locked),
					"事件等待父记录时不能抢占 Run，避免交互和终态反向等待")
				require.NoError(t, tx.Commit())
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				var count int
				require.NoError(t, f.db.QueryRowContext(ctx, "SELECT count(*) FROM agent_events WHERE run_id=$1 AND external_event_id='worker:1'", task.Claimed.RunID).Scan(&count))
				require.Equal(t, 1, count)
			})
		}
	}
}
