//go:build integration

package devcontainer

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/stretchr/testify/require"
)

func TestCompleteLocalProjectRelocationReconcilesOnlyUnreferencedTarget(t *testing.T) {
	ctx := context.Background()
	db := developmentDatabase(t)
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,enabled)
		VALUES ('relocation-guild',true);
		INSERT INTO discord_members(guild_id,discord_user_id,username,display_name)
		VALUES ('relocation-guild','relocation-owner','owner','Owner')`)
	require.NoError(t, err)
	manager := &Manager{db: db}

	t.Run("删除无引用的自动扫描目标", func(t *testing.T) {
		environmentID := insertRelocationEnvironment(t, db, "local-reconcile")
		sourceID := insertRelocationProject(t, db, environmentID,
			"workspaces/projects/notes-a1b2c3", "workspaces/notes")
		targetID := insertRelocationProject(t, db, environmentID, "workspaces/notes", "")

		require.NoError(t, manager.completeLocalProjectRelocation(ctx, sourceID.String()))
		var path string
		var dirty bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT relative_path,dirty
			FROM development_projects WHERE id=$1`, sourceID).Scan(&path, &dirty))
		require.Equal(t, "workspaces/notes", path)
		require.True(t, dirty)
		var targetCount int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM development_projects
			WHERE id=$1`, targetID).Scan(&targetCount))
		require.Zero(t, targetCount)
	})

	t.Run("不覆盖有 Forum 引用的目标", func(t *testing.T) {
		environmentID := insertRelocationEnvironment(t, db, "local-conflict")
		sourceID := insertRelocationProject(t, db, environmentID,
			"workspaces/projects/archive-d4e5f6", "workspaces/archive")
		targetID := insertRelocationProject(t, db, environmentID, "workspaces/archive", "")
		var resourceID uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_resources
			(guild_id,resource_key,discord_id,kind,name,managed_marker)
			VALUES ('relocation-guild',$1,$2,'forum','archive',$1) RETURNING id`,
			"forum.relocation."+targetID.String(), targetID.String()).Scan(&resourceID))
		_, err := db.ExecContext(ctx, `INSERT INTO discord_forums
			(guild_id,resource_id,forum_type,owner_discord_user_id,
			 development_project_id,development_environment_id)
			VALUES ('relocation-guild',$1,'development','owner-local-conflict',$2,$3)`,
			resourceID, targetID, environmentID)
		require.NoError(t, err)

		require.Error(t, manager.completeLocalProjectRelocation(ctx, sourceID.String()))
		var path, target string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT relative_path,desired_relative_path
			FROM development_projects WHERE id=$1`, sourceID).Scan(&path, &target))
		require.Equal(t, "workspaces/projects/archive-d4e5f6", path)
		require.Equal(t, "workspaces/archive", target)
	})
}

func insertRelocationEnvironment(t *testing.T, db *sql.DB, suffix string) uuid.UUID {
	t.Helper()
	ownerID := "owner-" + suffix
	_, err := db.ExecContext(context.Background(), `INSERT INTO discord_members
		(guild_id,discord_user_id,username,display_name) VALUES
		('relocation-guild',$1,$1,$1) ON CONFLICT DO NOTHING`, ownerID)
	require.NoError(t, err)
	var id uuid.UUID
	require.NoError(t, db.QueryRowContext(context.Background(),
		`INSERT INTO discord_development_environments
		(guild_id,owner_discord_user_id,container_name,data_volume_name,
		 home_volume_name,network_name,status)
		VALUES ('relocation-guild',$1,$2,$3,$4,$5,'running')
		RETURNING id`, ownerID, "container-"+suffix, "data-"+suffix, "home-"+suffix,
		"network-"+suffix).Scan(&id))
	return id
}

func insertRelocationProject(t *testing.T, db *sql.DB, environmentID uuid.UUID,
	relativePath, desiredPath string,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.QueryRowContext(context.Background(),
		`INSERT INTO development_projects
		(environment_id,relative_path,desired_relative_path,name,project_kind,
		 availability_status,dirty,last_seen_at)
		VALUES ($1,$2,NULLIF($3,''),'fixture','directory','available',true,now())
		RETURNING id`, environmentID, relativePath, desiredPath).Scan(&id))
	return id
}
