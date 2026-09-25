//go:build integration

package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

type sessionRuntimeFixture struct {
	db                   *sql.DB
	workspaceID          uuid.UUID
	clients              map[runtimeidentity.Engine]*workerprotocol.Client
	endpoint, credential string
}

func newSessionRuntimeFixture(t *testing.T) sessionRuntimeFixture {
	t.Helper()
	db := workerDatabase(t)
	ctx := t.Context()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "session-runtime", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	var workspaceID uuid.UUID
	_, err = db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,enabled) VALUES ('runtime-guild',true)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_members(guild_id,discord_user_id,username)
		VALUES ('runtime-guild','runtime-owner','owner')`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO worker_workspaces(worker_id,guild_id,owner_discord_user_id)
		VALUES ($1,'runtime-guild','runtime-owner') RETURNING id`, worker.ID).Scan(&workspaceID))
	_, err = db.ExecContext(ctx, `INSERT INTO workspace_projects(
		workspace_id,relative_path,name,project_kind,availability_status)
		VALUES ($1,'workspaces/project','project','git','available')`, workspaceID)
	require.NoError(t, err)
	base := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	clients := map[runtimeidentity.Engine]*workerprotocol.Client{}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		clients[engine], err = base.ForEngine(engine)
		require.NoError(t, err)
	}
	return sessionRuntimeFixture{db, workspaceID, clients, endpoint, credential}
}

func (f sessionRuntimeFixture) createThread(t *testing.T, engine runtimeidentity.Engine,
	key, threadID string,
) workerprotocol.DesktopThreadState {
	t.Helper()
	client := f.clients[engine]
	request := workerprotocol.DesktopThreadPrepareRequest{WorkspaceID: f.workspaceID,
		Operation: "start", RequestKey: strings.Repeat(key, 64),
		Params: json.RawMessage(`{"cwd":"/var/lib/tyrs-hand/project"}`)}
	state, err := client.PrepareDesktopThread(t.Context(), request)
	require.NoError(t, err)
	replay, err := client.PrepareDesktopThread(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, state.ID, replay.ID)
	response, err := json.Marshal(map[string]any{"thread": map[string]string{"id": threadID}, "model": "test-model"})
	require.NoError(t, err)
	state, err = client.CompleteDesktopThread(t.Context(), state.ID,
		workerprotocol.DesktopThreadCompleteRequest{WorkspaceID: f.workspaceID, Response: response})
	require.NoError(t, err)
	return state
}

func TestWorkerSessionRuntimeCreationAndForkIsolation(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	ctx := t.Context()
	codex := f.createThread(t, runtimeidentity.Codex, "a", "same-thread")
	claude := f.createThread(t, runtimeidentity.Claude, "a", "same-thread")
	require.NotEqual(t, codex.ID, claude.ID, "同一个创建键必须按引擎隔离")
	require.NotEqual(t, codex.ControlID, claude.ControlID)
	for engine, client := range f.clients {
		own, other := codex, claude
		if engine == runtimeidentity.Claude {
			own, other = claude, codex
		}
		state, err := client.DesktopThreadState(ctx, own.ID)
		require.NoError(t, err)
		require.Equal(t, own.ControlID, state.ControlID)
		_, err = client.DesktopThreadState(ctx, other.ID)
		assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
		err = client.FailDesktopThread(ctx, other.ID, workerprotocol.DesktopThreadFailRequest{
			WorkspaceID: f.workspaceID, Error: "must not change other runtime"})
		assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
		_, err = client.CompleteDesktopThread(ctx, other.ID, workerprotocol.DesktopThreadCompleteRequest{
			WorkspaceID: f.workspaceID, Response: json.RawMessage(`{"thread":{"id":"hijack"}}`)})
		assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
		fork, err := client.PrepareDesktopThread(ctx, workerprotocol.DesktopThreadPrepareRequest{
			WorkspaceID: f.workspaceID, Operation: "fork", RequestKey: strings.Repeat("b", 64),
			Params: json.RawMessage(`{"threadId":"same-thread"}`)})
		require.NoError(t, err)
		var source uuid.UUID
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT source_control_id FROM desktop_thread_requests WHERE id=$1`, fork.ID).Scan(&source))
		require.Equal(t, own.ControlID, source)
	}
	unique := f.createThread(t, runtimeidentity.Claude, "c", "claude-only")
	_, err := f.clients[runtimeidentity.Codex].PrepareDesktopThread(ctx, workerprotocol.DesktopThreadPrepareRequest{
		WorkspaceID: f.workspaceID, Operation: "fork", RequestKey: strings.Repeat("d", 64),
		Params: json.RawMessage(`{"threadId":"claude-only"}`)})
	assertRuntimeHTTPStatus(t, err, http.StatusForbidden)
	for _, table := range []string{"workspace_sessions", "codex_thread_controls", "desktop_thread_requests"} {
		_, err = f.db.ExecContext(ctx, "UPDATE "+table+" SET engine='codex' WHERE engine='claude-code'")
		require.Error(t, err, "创建之后不能切换引擎: "+table)
	}
	_, err = f.db.ExecContext(ctx, `UPDATE desktop_thread_requests SET control_id=$2 WHERE id=$1`, unique.ID, codex.ControlID)
	require.Error(t, err, "数据库必须拒绝跨引擎绑定")
	_, err = f.db.ExecContext(ctx, `UPDATE desktop_thread_requests SET source_control_id=$2 WHERE id=$1`, unique.ID, codex.ControlID)
	require.Error(t, err, "数据库必须拒绝跨引擎 fork 来源")
}

func assertRuntimeHTTPStatus(t *testing.T, err error, status int) {
	t.Helper()
	var response *workerprotocol.HTTPError
	require.ErrorAs(t, err, &response)
	require.Equal(t, status, response.StatusCode)
}
