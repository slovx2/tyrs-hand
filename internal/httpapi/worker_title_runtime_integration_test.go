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

func TestWorkerSessionTitleRuntimeIsolation(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	ctx := t.Context()
	sessions := map[runtimeidentity.Engine]uuid.UUID{}
	for engine := range f.clients {
		state := f.createThread(t, engine, "a", "same-thread")
		var sessionID uuid.UUID
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT session_id FROM codex_thread_controls WHERE id=$1`, state.ControlID).Scan(&sessionID))
		sessions[engine] = sessionID
		tx, err := f.db.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, inserted, err := codexcontrol.NewRepository(f.db, time.Minute).Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
			SourceType: codexcontrol.SourceWorkspace, SessionID: sessionID, InputSurface: "client",
			IdempotencyKey: "title-" + string(engine), MessageLocalID: "same-message", Instruction: "同名任务",
			Behavior: "start_when_idle", ReplyPolicy: "silent",
		})
		require.NoError(t, err)
		require.True(t, inserted)
		require.NoError(t, tx.Commit())
	}
	claims := map[runtimeidentity.Engine]*workerprotocol.SessionTitleTask{}
	for engine, client := range f.clients {
		claim, err := client.ClaimSessionTitle(ctx)
		require.NoError(t, err)
		require.NotNil(t, claim.Task)
		require.Equal(t, engine, claim.Task.Engine)
		require.Equal(t, sessions[engine], claim.Task.SessionID)
		claims[engine] = claim.Task
	}
	claude := claims[runtimeidentity.Claude]
	codexClient := f.clients[runtimeidentity.Codex]
	completion := workerprotocol.SessionTitleCompleteRequest{
		LeaseToken: claude.LeaseToken, TitleRevision: claude.TitleRevision, Title: "Claude 标题",
	}
	assertRuntimeHTTPStatus(t, codexClient.CompleteSessionTitle(ctx, claude.ID, completion), http.StatusConflict)
	assertRuntimeHTTPStatus(t, codexClient.FailSessionTitle(ctx, claude.ID,
		workerprotocol.SessionTitleFailRequest{LeaseToken: claude.LeaseToken, ErrorCode: "timeout"}), http.StatusConflict)
	require.NoError(t, f.clients[runtimeidentity.Claude].CompleteSessionTitle(ctx, claude.ID, completion))
	var source, title string
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT title_source,title FROM workspace_sessions WHERE id=$1`, sessions[runtimeidentity.Claude]).Scan(&source, &title))
	require.Equal(t, "generated", source)
	require.Equal(t, "Claude 标题", title)
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT title_source FROM workspace_sessions WHERE id=$1`, sessions[runtimeidentity.Codex]).Scan(&source))
	require.Equal(t, "generating", source, "Claude 标题完成不能改变 Codex 标题任务")
}
