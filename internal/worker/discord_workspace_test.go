package worker

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/stretchr/testify/require"
)

func TestDiscordConversationsReuseOwnerRepositoryWorkspace(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	mock.MatchExpectationsInOrder(false)
	mock.ExpectClose()
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	processor := &Processor{db: db, cfg: config.Config{DiscordWorkspaceRoot: "/data/worker/workspaces/discord"}}
	repositoryID := uuid.New()
	workspaceID := uuid.New()
	path := "/data/worker/workspaces/discord/" + workspaceID.String()
	branch := "tyrs-hand/discord/" + workspaceID.String()

	first := discordJobContext{ConversationID: uuid.New(), GuildID: "guild", OwnerUserID: "owner", RepositoryID: repositoryID}
	second := first
	second.ConversationID = uuid.New()
	expectDiscordWorkspaceBinding(mock, first, workspaceID, path, branch)
	expectDiscordWorkspaceBinding(mock, second, workspaceID, path, branch)

	firstID, firstPath, firstBranch, err := processor.bindDiscordWorkspace(context.Background(), first)
	require.NoError(t, err)
	secondID, secondPath, secondBranch, err := processor.bindDiscordWorkspace(context.Background(), second)
	require.NoError(t, err)
	require.Equal(t, firstID, secondID)
	require.Equal(t, firstPath, secondPath)
	require.Equal(t, firstBranch, secondBranch)
}

func TestDiscordOwnersUseIsolatedWorkspaces(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	mock.MatchExpectationsInOrder(false)
	mock.ExpectClose()
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	processor := &Processor{db: db, cfg: config.Config{DiscordWorkspaceRoot: "/data/worker/workspaces/discord"}}
	repositoryID := uuid.New()
	first := discordJobContext{ConversationID: uuid.New(), GuildID: "guild", OwnerUserID: "owner-1", RepositoryID: repositoryID}
	second := discordJobContext{ConversationID: uuid.New(), GuildID: "guild", OwnerUserID: "owner-2", RepositoryID: repositoryID}
	firstWorkspace := uuid.New()
	secondWorkspace := uuid.New()
	expectDiscordWorkspaceBinding(mock, first, firstWorkspace, "/workspace/one", "tyrs-hand/discord/"+firstWorkspace.String())
	expectDiscordWorkspaceBinding(mock, second, secondWorkspace, "/workspace/two", "tyrs-hand/discord/"+secondWorkspace.String())

	firstID, firstPath, _, err := processor.bindDiscordWorkspace(context.Background(), first)
	require.NoError(t, err)
	secondID, secondPath, _, err := processor.bindDiscordWorkspace(context.Background(), second)
	require.NoError(t, err)
	require.NotEqual(t, firstID, secondID)
	require.NotEqual(t, firstPath, secondPath)
}

func expectDiscordWorkspaceBinding(mock sqlmock.Sqlmock, job discordJobContext, workspaceID uuid.UUID, path, branch string) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO discord_workspaces")).
		WithArgs(sqlmock.AnyArg(), job.GuildID, job.OwnerUserID, job.RepositoryID, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "path", "branch"}).AddRow(workspaceID, path, branch))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE discord_conversations SET workspace_id = $2, updated_at = now()")).
		WithArgs(job.ConversationID, workspaceID, job.RepositoryID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}
