//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func queueDesktopRequest(workspace uuid.UUID) workerprotocol.DesktopTurnPrepareRequest {
	return workerprotocol.DesktopTurnPrepareRequest{WorkspaceID: workspace, RunID: uuid.New(), IntentID: uuid.New(),
		TurnID: "native-queue-next-turn", RequestKey: strings.Repeat("b", 64),
		Params: json.RawMessage(`{"threadId":"same-thread","input":[{"type":"text","text":"原生队列下一回合"}]}`)}
}

func TestWorkerDesktopQueueReconcilesPreviousActiveRun(t *testing.T) {
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		t.Run(string(engine), func(t *testing.T) {
			f := newSessionRuntimeFixture(t)
			previous := f.startRun(t, engine)
			ctx := t.Context()
			var previousStatus string
			require.NoError(t, f.db.QueryRowContext(ctx, "SELECT status FROM codex_thread_controls WHERE id=$1", previous.Claimed.ControlID).Scan(&previousStatus))
			request := queueDesktopRequest(f.workspaceID)
			next, err := f.clients[engine].PrepareDesktopTurn(ctx, request)
			require.NoError(t, err, "上一回合未完成补报时，下一实际回合必须能登记")
			require.Equal(t, previous.Claimed.ControlID, next.Claimed.ControlID)
			var oldStatus string
			var active bool
			require.NoError(t, f.db.QueryRowContext(ctx, "SELECT status,active_slot IS NOT NULL FROM codex_turn_runs WHERE id=$1", previous.Claimed.RunID).Scan(&oldStatus, &active))
			require.Equal(t, "reconciling", oldStatus)
			require.False(t, active)
			var auditStatus string
			require.NoError(t, f.db.QueryRowContext(ctx, `SELECT metadata->>'previousStatus' FROM audit_logs
				WHERE action='worker.run.reconciled' AND resource_id=$1 AND metadata->>'runId'=$2`,
				previous.Claimed.ControlID.String(), request.RunID.String()).Scan(&auditStatus))
			require.Equal(t, previousStatus, auditStatus)
			replay, err := f.clients[engine].PrepareDesktopTurn(ctx, request)
			require.NoError(t, err)
			require.Equal(t, next.Claimed.RunID, replay.Claimed.RunID)
		})
	}
}

func TestWorkerDesktopTurnLocksSessionBeforeControl(t *testing.T) {
	verifyWorkerDesktopLocksSessionBeforeControl(t, false)
}

func TestWorkerDesktopSteerLocksSessionBeforeControl(t *testing.T) {
	verifyWorkerDesktopLocksSessionBeforeControl(t, true)
}

func verifyWorkerDesktopLocksSessionBeforeControl(t *testing.T, steer bool) {
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		t.Run(string(engine), func(t *testing.T) {
			f := newSessionRuntimeFixture(t)
			previous := f.startRun(t, engine)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			tx, err := f.db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			var pid int
			var locked uuid.UUID
			require.NoError(t, tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
			require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM workspace_sessions WHERE id=$1 FOR UPDATE", previous.Claimed.SessionID).Scan(&locked))
			done := make(chan error, 1)
			go func() {
				if steer {
					done <- f.clients[engine].RecordDesktopSteer(ctx, workerprotocol.DesktopSteerRecordRequest{
						WorkspaceID: f.workspaceID, RequestKey: strings.Repeat("c", 64),
						Params: json.RawMessage(`{"threadId":"same-thread","expectedTurnId":"running-turn","input":[{"type":"text","text":"继续执行"}]}`),
					})
					return
				}
				_, err := f.clients[engine].PrepareDesktopTurn(ctx, queueDesktopRequest(f.workspaceID))
				done <- err
			}()
			require.Eventually(t, func() bool {
				var waiting bool
				return f.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", pid).Scan(&waiting) == nil && waiting
			}, 3*time.Second, 10*time.Millisecond)
			require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM codex_thread_controls WHERE id=$1 FOR UPDATE NOWAIT", previous.Claimed.ControlID).Scan(&locked), "不能持有 Control 等待 Session，与事件路径形成反向等待")
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
