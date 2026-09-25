//go:build integration

package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func TestLiveCannotBindOrDispatchToClaudeRuntime(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	ctx := t.Context()
	server := &Server{db: f.db}
	var workerID uuid.UUID
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT worker_id FROM worker_workspaces WHERE id=$1`, f.workspaceID).Scan(&workerID))
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		thread := f.createThread(t, engine, "a", "same-thread")
		var sessionID uuid.UUID
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT session_id FROM codex_thread_controls WHERE id=$1`, thread.ControlID).Scan(&sessionID))
		_, err := f.db.ExecContext(ctx, `UPDATE workspace_sessions SET title=$2 WHERE id=$1`, sessionID, string(engine)+"-visible-title")
		require.NoError(t, err)
		tx, err := f.db.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, _, bindingErr := server.liveSessionBinding(ctx, tx, sessionID)
		require.NoError(t, tx.Rollback())
		accessErr := server.ensureSessionOnWorker(ctx, sessionID, workerID)
		if engine == runtimeidentity.Codex {
			require.NoError(t, bindingErr)
			require.NoError(t, accessErr)
		} else {
			require.EqualError(t, bindingErr, "不支持在 Claude 会话中使用 Live 语音")
			require.EqualError(t, accessErr, "不支持在 Claude 会话中使用 Live 语音")
		}
	}
	result, err := server.liveVoiceListSessions(ctx, workerID)
	require.NoError(t, err)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "codex-visible-title")
	require.NotContains(t, string(encoded), "claude-code-visible-title")
}
