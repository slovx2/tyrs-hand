//go:build integration

package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

// TestPendingThreadNamesSkipsArchivedControl 验证归档 Thread 的 fallback 标题
// 不再进入待应用集合。归档后 rollout 位于 archived_sessions，
// Codex thread/name/set 找不到该 Thread，重试永远不会成功。
func TestPendingThreadNamesSkipsArchivedControl(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "thread-name-archive",
		[]string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	var profileID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT id FROM agent_profiles WHERE name = 'Default'`).Scan(&profileID))

	var workspaceID uuid.UUID
	_, err = db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, enabled)
		VALUES ('thread-name-guild', true)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_members
		(guild_id, discord_user_id, username, display_name)
		VALUES ('thread-name-guild','thread-name-owner','owner','Owner')`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO worker_workspaces(
		guild_id, owner_discord_user_id, worker_id)
		VALUES ('thread-name-guild','thread-name-owner',$1) RETURNING id`, worker.ID).
		Scan(&workspaceID))
	var projectID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_projects(
		workspace_id,relative_path,name,project_kind,availability_status,
		 branch,head_sha,dirty,last_seen_at)
		VALUES ($1,'project','project','git','available','main','sha',false,now())
		RETURNING id`, workspaceID).Scan(&projectID))
	insertControl := func(state string) uuid.UUID {
		t.Helper()
		var sessionID uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
			workspace_id,workspace_project_id,agent_profile_id,title)
			VALUES ($1,$2,$3,'Title') RETURNING id`, workspaceID, projectID, profileID).
			Scan(&sessionID))
		var id uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_thread_controls(
			source_type,session_id,workspace_project_id,agent_profile_id,worker_id,
			workspace_id,external_thread_id,desired_thread_name,
			desired_thread_name_source,desired_thread_name_revision,
			applied_thread_name_revision,lifecycle_state)
			VALUES ('workspace_session',$1,$2,$3,$4,$5,$6,'Title','fallback',1,0,$7)
			RETURNING id`, sessionID, projectID, profileID, worker.ID, workspaceID,
			"thread-"+state, state).Scan(&id))
		return id
	}
	active := insertControl("active")
	archived := insertControl("archived")

	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	updates, err := client.PendingThreadNames(ctx)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	require.Equal(t, active, updates[0].ControlID)
	require.NotEqual(t, archived, updates[0].ControlID)

	targets, err := server.resolveWorkerWakeTargets(ctx, workerprotocol.WakeThreadSync)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{worker.ID}, targets)
}
