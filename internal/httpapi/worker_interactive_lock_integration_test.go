//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/interactiveprotocol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

// 正式矩阵的 PG 证据：恢复审批持有 Run 等待 Control，事件路径持有 Control 等待 Run。
func TestWorkerInteractiveLocksParentsBeforeRun(t *testing.T) {
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		for _, operation := range []string{"register", "resume"} {
			for _, parent := range []string{"session", "control", "intent"} {
				t.Run(string(engine)+"/"+operation+"/"+parent, func(t *testing.T) {
					f := newSessionRuntimeFixture(t)
					ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
					defer cancel()
					task := f.startRun(t, engine)
					client := f.clients[engine]
					register := func() (workerprotocol.InteractiveState, error) {
						return client.RegisterInteractive(ctx, task, interactiveprotocol.CommandApproval,
							json.RawMessage(`1`), json.RawMessage(`{"threadId":"same-thread","turnId":"lock-turn","itemId":"lock-item","command":"printf approved"}`), 1)
					}
					invoke := register
					if operation == "resume" {
						state, err := register()
						require.NoError(t, err)
						_, err = f.db.ExecContext(ctx, `UPDATE codex_interactive_requests SET
							status='resolved',answer='{"decision":"accept"}',answer_surface='desktop',resolved_at=now() WHERE id=$1`, state.ID)
						require.NoError(t, err)
						invoke = func() (workerprotocol.InteractiveState, error) { return client.InteractiveState(ctx, state.ID) }
					}
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
						state, callErr := invoke()
						if callErr == nil && operation == "resume" && !state.Ready {
							callErr = context.DeadlineExceeded
						}
						done <- callErr
					}()
					require.Eventually(t, func() bool {
						var waiting bool
						return f.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", pid).Scan(&waiting) == nil && waiting
					}, 3*time.Second, 10*time.Millisecond, "交互操作须先等待父记录")
					require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM codex_turn_runs WHERE id=$1 FOR UPDATE NOWAIT", task.Claimed.RunID).Scan(&locked), "等待父记录时不能抢占 Run")
					require.NoError(t, tx.Commit())
					select {
					case err := <-done:
						require.NoError(t, err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				})
			}
		}
	}
}
