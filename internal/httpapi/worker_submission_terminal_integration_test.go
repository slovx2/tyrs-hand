//go:build integration

package httpapi

import (
	"net/http"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func TestWorkerLateSubmissionCannotReopenTerminalRun(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	ctx := t.Context()
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		task := f.startRun(t, engine)
		client := f.clients[engine]
		require.NoError(t, client.RecordSubmission(ctx, task, "submission"))
		require.NoError(t, client.ConfirmTurn(ctx, task, "turn"))
		require.NoError(t, client.RecordSubmission(ctx, task, "submission"))
		var status string
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT status FROM codex_turn_intents WHERE id=$1`, task.Claimed.ID).Scan(&status))
		require.Equal(t, "running", status, "迟到 submission 不能降级已确认状态")
		require.NoError(t, client.Complete(ctx, task, codexcontrol.TurnResult{FinalAnswer: "done", TurnID: "turn"}))
		require.NoError(t, client.RecordSubmission(ctx, task, "submission"))
		require.NoError(t, client.ConfirmTurn(ctx, task, "turn"))
		var runStatus, controlStatus, remoteStatus, activeTurn string
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT i.status,r.status,c.status,c.remote_status,COALESCE(c.active_codex_turn_id,'')
			FROM codex_turn_intents i JOIN codex_turn_runs r ON r.primary_intent_id=i.id
			JOIN codex_thread_controls c ON c.id=i.control_id WHERE r.id=$1`, task.Claimed.RunID).
			Scan(&status, &runStatus, &controlStatus, &remoteStatus, &activeTurn))
		require.Equal(t, "completed", status)
		require.Equal(t, "completed", runStatus)
		require.Equal(t, "idle", controlStatus)
		require.Equal(t, "idle", remoteStatus)
		require.Empty(t, activeTurn)
		assertRuntimeHTTPStatus(t, client.RecordSubmission(ctx, task, "changed-submission"), http.StatusConflict)
		assertRuntimeHTTPStatus(t, client.ConfirmTurn(ctx, task, "changed-turn"), http.StatusConflict)
	}
}
