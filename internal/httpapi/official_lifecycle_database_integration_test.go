//go:build integration

package httpapi

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
	"github.com/stretchr/testify/require"
)

func TestDiscoveredOfficialThreadReconcilesLifecycleIdempotently(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))

	const (
		guildID       = "100000000000000901"
		forumChannel  = "100000000000000902"
		discordThread = "100000000000000903"
		officialID    = "thread-discovery-lifecycle"
	)
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,name,enabled)
		VALUES($1,'official-lifecycle-discovery',true)`, guildID)
	require.NoError(t, err)
	workers := workerregistry.NewService(db)
	worker, _, err := workers.Create(ctx, "official-lifecycle-worker", []string{"discord"}, 1)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE workers SET
		metadata='{"host":{"workspaceRoot":"/workspace"}}'::jsonb WHERE id=$1`, worker.ID)
	require.NoError(t, err)

	var workspaceID, projectID, resourceID, forumID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO worker_workspaces(
		guild_id,owner_discord_user_id,worker_id) VALUES($1,'owner',$2) RETURNING id`,
		guildID, worker.ID).Scan(&workspaceID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_projects(
		workspace_id,relative_path,name,project_kind,availability_status)
		VALUES($1,'workspaces/project','project','git','available') RETURNING id`, workspaceID).
		Scan(&projectID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_resources(
		guild_id,resource_key,discord_id,kind,name,managed_marker)
		VALUES($1,'forum.lifecycle',$2,'forum','lifecycle','[test]') RETURNING id`,
		guildID, forumChannel).Scan(&resourceID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_forums(
		guild_id,resource_id,forum_type,owner_discord_user_id,workspace_id,
		workspace_project_id) VALUES($1,$2,'workspace','owner',$3,$4) RETURNING id`,
		guildID, resourceID, workspaceID, projectID).Scan(&forumID))

	conversationID, err := discordintegration.NewConversationService(db).BeginPost(ctx,
		discordintegration.IncomingMessage{GuildID: guildID, ForumID: forumChannel,
			ThreadID: discordThread, MessageID: "100000000000000904",
			DiscordUserID: "owner", DisplayName: "Owner", Username: "owner",
			Title: "Lifecycle", Body: "lifecycle", ConfigurationConfirmed: true})
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO official_thread_bindings(
		workspace_id,conversation_id,workspace_project_id,thread_id)
		VALUES($1,$2,$3,$4)`, workspaceID, conversationID, projectID, officialID)
	require.NoError(t, err)

	server := &Server{db: db}
	thread := officialapp.Thread{ID: officialID, CWD: "/workspace/project"}
	bound, err := server.bindDiscoveredOfficialThread(ctx, workspaceID, thread, true)
	require.NoError(t, err)
	require.True(t, bound)
	assertOfficialLifecycle(t, ctx, db, conversationID, "archived", 1)
	assertLifecycleOutboxRevision(t, ctx, db,
		"conversation-lifecycle-card:"+conversationID.String(), 1)

	_, err = server.bindDiscoveredOfficialThread(ctx, workspaceID, thread, true)
	require.NoError(t, err)
	assertOfficialLifecycle(t, ctx, db, conversationID, "archived", 1)
	assertLifecycleOutboxRevision(t, ctx, db,
		"conversation-lifecycle-card:"+conversationID.String(), 1)

	_, err = server.bindDiscoveredOfficialThread(ctx, workspaceID, thread, false)
	require.NoError(t, err)
	assertOfficialLifecycle(t, ctx, db, conversationID, "active", 2)
	assertLifecycleOutboxRevision(t, ctx, db,
		"conversation-lifecycle:"+conversationID.String(), 1)
	_, err = server.bindDiscoveredOfficialThread(ctx, workspaceID, thread, false)
	require.NoError(t, err)
	assertOfficialLifecycle(t, ctx, db, conversationID, "active", 2)
	assertLifecycleOutboxRevision(t, ctx, db,
		"conversation-lifecycle:"+conversationID.String(), 1)
}

func assertOfficialLifecycle(t *testing.T, ctx context.Context, db *sql.DB,
	conversationID uuid.UUID, expected string, revision int64,
) {
	t.Helper()
	var state string
	var actualRevision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state,lifecycle_revision
		FROM discord_conversations WHERE id=$1`, conversationID).Scan(&state, &actualRevision))
	require.Equal(t, expected, state)
	require.Equal(t, revision, actualRevision)
}

func assertLifecycleOutboxRevision(t *testing.T, ctx context.Context, db *sql.DB,
	operationKey string, revision int64,
) {
	t.Helper()
	var actual int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT request_revision
		FROM integration_outbox WHERE operation_key=$1`, operationKey).Scan(&actual))
	require.Equal(t, revision, actual)
}
