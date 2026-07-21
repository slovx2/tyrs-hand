package discordintegration

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSanitizeDiscordResult(t *testing.T) {
	value := SanitizeDiscordResult("完成 /Volumes/workspace/private/file.go，token=ghp_abcdefghijklmnopqrstuvwxyz")
	require.NotContains(t, value, "/Volumes/workspace")
	require.NotContains(t, value, "ghp_")
	require.Contains(t, value, "[已隐藏路径]")
	require.Contains(t, value, "[已隐藏凭据]")

	long := SanitizeDiscordResult(strings.Repeat("你", 2100))
	require.LessOrEqual(t, len([]rune(long)), 1910)
	require.Contains(t, long, "内容已截断")
}

func TestValidateIncomingMessageBoundaries(t *testing.T) {
	base := IncomingMessage{GuildID: "1", ThreadID: "2", MessageID: "3", DiscordUserID: "4", Body: "hello"}
	require.NoError(t, validateIncomingMessage(base))
	missing := base
	missing.MessageID = ""
	require.Error(t, validateIncomingMessage(missing))
	empty := base
	empty.Body = "  "
	require.Error(t, validateIncomingMessage(empty))
	empty.Attachments = []IncomingAttachment{{ID: "1"}}
	require.NoError(t, validateIncomingMessage(empty))
	tooMany := base
	tooMany.Attachments = make([]IncomingAttachment, DefaultMaxAttachments+1)
	require.Error(t, validateIncomingMessage(tooMany))
}

func TestProjectConversationStatusUsesSingleProjectionOutboxKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	conversationID := uuid.New()
	projectionKey := "conversation:" + conversationID.String() + ":message:message-1"
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO discord_projections")).
		WithArgs("guild-1", projectionKey, "thread-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"resource_id", "message_id"}).AddRow("thread-1", ""))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO integration_outbox")).
		WithArgs("projection:"+projectionKey, "message.create", "channels/thread-1/messages",
			sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err = ProjectConversationStatus(context.Background(), db, "guild-1", "thread-1",
		conversationID, "message-1", uuid.Nil, ConversationRunning, "消息已进入队列。")
	require.NoError(t, err)
	mock.ExpectClose()
}
