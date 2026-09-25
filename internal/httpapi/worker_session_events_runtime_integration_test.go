//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWorkerSessionRuntimeMetadataAndLifecycleIsolation(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	ctx := t.Context()
	states := map[runtimeidentity.Engine]workerprotocol.DesktopThreadState{}
	for engine := range f.clients {
		states[engine] = f.createThread(t, engine, "a", "same-thread")
	}
	// 两引擎使用完全相同的 generation/sequence，事件去重仍分别生效。
	for engine, client := range f.clients {
		metadata := workerprotocol.ThreadMetadataRequest{WorkspaceID: f.workspaceID, Generation: 1,
			Events: []workerprotocol.ThreadMetadataEvent{
				{Kind: "name", ThreadID: "same-thread", Sequence: 1, Name: string(engine)},
				{Kind: "settings", ThreadID: "same-thread", Sequence: 2,
					Model: "model-" + string(engine), Source: "desktop", ServiceTier: "standard"},
			}}
		require.NoError(t, client.RecordThreadMetadata(ctx, metadata))
		require.NoError(t, client.RecordThreadMetadata(ctx, metadata))
	}
	for engine, state := range states {
		var name, model string
		var revision int64
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT session.title,session.model,
			control.desired_thread_name_revision FROM workspace_sessions session
			JOIN codex_thread_controls control ON control.session_id=session.id WHERE control.id=$1`,
			state.ControlID).Scan(&name, &model, &revision))
		require.Equal(t, string(engine), name)
		require.Equal(t, "model-"+string(engine), model)
		require.EqualValues(t, 1, revision)
	}
	_, err := f.db.ExecContext(ctx, `UPDATE codex_thread_controls SET desired_thread_name_source='fallback'`)
	require.NoError(t, err)
	for engine, client := range f.clients {
		updates, err := client.PendingThreadNames(ctx)
		require.NoError(t, err)
		require.Len(t, updates, 1)
		require.Equal(t, states[engine].ControlID, updates[0].ControlID)
	}
	codexClient, claudeClient := f.clients[runtimeidentity.Codex], f.clients[runtimeidentity.Claude]
	ack := workerprotocol.ThreadNameAckRequest{WorkspaceID: f.workspaceID, Revision: 1}
	assertRuntimeHTTPStatus(t, claudeClient.AckThreadName(ctx, states[runtimeidentity.Codex].ControlID, ack), http.StatusNotFound)
	require.NoError(t, claudeClient.AckThreadName(ctx, states[runtimeidentity.Claude].ControlID, ack))
	updates, err := codexClient.PendingThreadNames(ctx)
	require.NoError(t, err)
	require.Len(t, updates, 1, "Claude ack 不应确认 Codex 标题")
	claudeLifecycle, err := claudeClient.PrepareDesktopThreadLifecycle(ctx,
		workerprotocol.ThreadLifecyclePrepareRequest{WorkspaceID: f.workspaceID, ThreadID: "same-thread", DesiredState: "archived"})
	require.NoError(t, err)
	require.Equal(t, states[runtimeidentity.Claude].ControlID, claudeLifecycle.ControlID)
	_, err = codexClient.ThreadLifecycleState(ctx, claudeLifecycle.ID)
	assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
	completion := workerprotocol.ThreadLifecycleCompleteRequest{WorkspaceID: f.workspaceID, Response: json.RawMessage(`{}`)}
	assertRuntimeHTTPStatus(t, codexClient.CompleteThreadLifecycle(ctx, claudeLifecycle.ID, completion), http.StatusNotFound)
	require.NoError(t, claudeClient.CompleteThreadLifecycle(ctx, claudeLifecycle.ID, completion))
	require.NoError(t, claudeClient.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: f.workspaceID, Generation: 1, Events: []workerprotocol.ThreadMetadataEvent{
			{Kind: "lifecycle", ThreadID: "same-thread", Sequence: 3, LifecycleState: "archived"},
		}}))
	for engine, state := range states {
		var lifecycle string
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT lifecycle_state FROM codex_thread_controls WHERE id=$1`, state.ControlID).Scan(&lifecycle))
		want := "active"
		if engine == runtimeidentity.Claude {
			want = "archived"
		}
		require.Equal(t, want, lifecycle)
	}
	// 待应用队列的推进和读取都必须限定引擎。
	_, err = f.db.ExecContext(ctx, `UPDATE codex_thread_controls SET lifecycle_revision=lifecycle_revision+1`)
	require.NoError(t, err)
	for engine, state := range states {
		_, err := f.db.ExecContext(ctx, `INSERT INTO codex_thread_lifecycle_requests
			(id,control_id,workspace_id,source,desired_state,status,revision)
			SELECT $1,id,workspace_id,'client','active','waiting_for_turn',lifecycle_revision
			FROM codex_thread_controls WHERE id=$2`, uuid.New(), state.ControlID)
		require.NoError(t, err, string(engine))
	}
	for engine, client := range f.clients {
		pending, err := client.PendingThreadLifecycles(ctx)
		require.NoError(t, err)
		require.Len(t, pending, 1)
		require.Equal(t, states[engine].ControlID, pending[0].ControlID)
	}
}

func TestWorkerRuntimeScopeRejectsMissingUnknownAndUnsupported(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	for _, engine := range []string{"", "gpt", "CLAUDE-CODE"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			f.endpoint+"/worker/v1/thread-name-updates", nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+f.credential)
		request.Header.Set(workerprotocol.VersionHeader, "33")
		request.Header.Set(workerprotocol.EngineHeader, engine)
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, response.StatusCode)
		require.NoError(t, response.Body.Close())
	}
	_, err := f.clients[runtimeidentity.Claude].Identity(t.Context())
	assertRuntimeHTTPStatus(t, err, http.StatusNotImplemented)
}
