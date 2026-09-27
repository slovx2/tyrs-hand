//go:build integration

package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWorkerMetadataLocksSessionBeforeControl(t *testing.T) {
	for _, event := range []workerprotocol.ThreadMetadataEvent{
		{Kind: "name", Name: "同步标题"},
		{Kind: "settings", Model: "test-model", Source: "desktop"},
		{Kind: "lifecycle", LifecycleState: "archived"},
	} {
		t.Run(event.Kind, func(t *testing.T) {
			f := newSessionRuntimeFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			state := f.createThread(t, runtimeidentity.Codex, "a", "metadata-lock-thread")
			tx, err := f.db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			var backendPID int
			var locked uuid.UUID
			require.NoError(t, tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT session.id FROM workspace_sessions session
				JOIN codex_thread_controls control ON control.session_id=session.id
				WHERE control.id=$1 FOR UPDATE OF session`, state.ControlID).Scan(&locked))
			event.ThreadID, event.Sequence = "metadata-lock-thread", 1
			done := make(chan error, 1)
			go func() {
				done <- f.clients[runtimeidentity.Codex].RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
					WorkspaceID: f.workspaceID, Generation: 1, Events: []workerprotocol.ThreadMetadataEvent{event},
				})
			}()
			require.Eventually(t, func() bool {
				var blocked bool
				return f.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))",
					backendPID).Scan(&blocked) == nil && blocked
			}, 3*time.Second, 10*time.Millisecond, "metadata 必须已到达真实 Session 行锁")
			// Live 入队先持有 Session；metadata 不能提前占用 Control 形成反向等待。
			require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM codex_thread_controls WHERE id=$1 FOR UPDATE NOWAIT",
				state.ControlID).Scan(&locked))
			require.NoError(t, tx.Commit())
			select {
			case result := <-done:
				require.NoError(t, result)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}
