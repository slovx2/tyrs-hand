//go:build integration

package discordintegration

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/stretchr/testify/require"
)

func TestMemberCanOwnWorkspacesOnMultipleWorkers(t *testing.T) {
	ctx := context.Background()
	db := discordDatabase(t)
	require.NoError(t, database.Migrate(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	manager := NewManager(db, secrets.NewStore(db, box))
	require.NoError(t, manager.SaveSettings(ctx, SettingsInput{
		GuildID: testGuildID, Enabled: true, BotToken: "test-token",
		ApplicationID: "100000000000000002", BotUserID: testBotID,
	}))
	seed := seedDiscordManagerData(t, db)
	var secondWorker uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workers(name, roles)
		VALUES ('second-workspace-worker', '["discord"]') RETURNING id`).Scan(&secondWorker))

	secondWorkspace, err := manager.CreateWorkspace(ctx, "1001", secondWorker)
	require.NoError(t, err)
	require.NotEqual(t, seed.workspaceID, secondWorkspace)
	for workerID, workspaceID := range map[uuid.UUID]uuid.UUID{
		seed.workerID: seed.workspaceID, secondWorker: secondWorkspace,
	} {
		workspace, readErr := manager.WorkspaceForWorker(ctx, workerID)
		require.NoError(t, readErr)
		require.NotNil(t, workspace)
		require.Equal(t, workspaceID, workspace.ID)
		require.Equal(t, "1001", workspace.OwnerUserID)
	}

	// 多 Workspace 负责人仍只出现在成员列表中一次。
	members, err := manager.Members(ctx)
	require.NoError(t, err)
	require.Len(t, members, 3)
	_, err = manager.CreateWorkspace(ctx, "1002", secondWorker)
	require.ErrorContains(t, err, "worker 已绑定 Workspace")

	_, err = db.ExecContext(ctx, `UPDATE discord_members SET active=false
		WHERE guild_id=$1 AND discord_user_id='1002'`, testGuildID)
	require.NoError(t, err)
	_, err = manager.CreateWorkspace(ctx, "1002", uuid.New())
	require.ErrorContains(t, err, "成员不存在、不活跃或为机器人")
	_, err = db.ExecContext(ctx, `UPDATE discord_members SET is_bot=true
		WHERE guild_id=$1 AND discord_user_id='1003'`, testGuildID)
	require.NoError(t, err)
	_, err = manager.CreateWorkspace(ctx, "1003", uuid.New())
	require.ErrorContains(t, err, "成员不存在、不活跃或为机器人")
}
