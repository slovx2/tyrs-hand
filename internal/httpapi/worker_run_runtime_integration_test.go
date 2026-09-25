//go:build integration

package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWorkerRuntimeRejectsForeignRunAndAttachments(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	ctx := t.Context()
	state := f.createThread(t, runtimeidentity.Claude, "a", "same-thread")
	var sessionID, workerID uuid.UUID
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT session_id,worker_id FROM codex_thread_controls WHERE id=$1`, state.ControlID).Scan(&sessionID, &workerID))
	repository := codexcontrol.NewRepository(f.db, time.Minute)
	tx, err := f.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	intentID, inserted, err := repository.Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceWorkspace, SessionID: sessionID, InputSurface: "client",
		IdempotencyKey: "runtime-input", Instruction: "Claude only", Behavior: "start_when_idle", ReplyPolicy: "silent",
	})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	// Codex 领取不会拿走 Claude 输入，直接伪造其 ID 也不能登记 Run。
	queued, err := repository.PendingWorkerInput(ctx, workerID, runtimeidentity.Codex, codexcontrol.WorkerInputSelection{})
	require.NoError(t, err)
	require.Nil(t, queued)
	runID := uuid.New()
	require.Error(t, repository.StartWorkerInput(ctx, workerID, intentID, runID, runtimeidentity.Codex))
	require.NoError(t, repository.StartWorkerInput(ctx, workerID, intentID, runID, runtimeidentity.Claude))
	var interactiveID uuid.UUID
	require.NoError(t, f.db.QueryRowContext(ctx, `INSERT INTO codex_interactive_requests(
		control_id,run_id,session_id,thread_id,turn_id,item_id,app_server_generation,app_server_request_id,questions,
		deadline_at) VALUES ($1,$2,$3,'same-thread','same-turn','same-item',1,'1','[]',now()-interval '1 second') RETURNING id`,
		state.ControlID, runID, sessionID).Scan(&interactiveID))
	client := f.clients[runtimeidentity.Codex]
	_, err = client.RunHeartbeat(ctx, &workerprotocol.Task{Claimed: codexcontrol.ClaimedControl{RunID: runID}})
	assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
	_, err = client.DesktopImageTarget(ctx, intentID)
	assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
	err = client.CompleteDesktopRollback(ctx, intentID, workerprotocol.DesktopRollbackCompleteRequest{
		WorkspaceID: f.workspaceID, Error: "must not cancel Claude input",
	})
	assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
	_, err = client.InteractiveState(ctx, interactiveID)
	assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
	var status string
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT status FROM codex_interactive_requests WHERE id=$1`, interactiveID).Scan(&status))
	require.Equal(t, "pending", status, "跨引擎读取不能触发过期处理或恢复调度槽")
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT status FROM codex_turn_intents WHERE id=$1`, intentID).Scan(&status))
	require.Equal(t, "dispatching", status, "跨引擎 rollback 不能取消原输入")
}
