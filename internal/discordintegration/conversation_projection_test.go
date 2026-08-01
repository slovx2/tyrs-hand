package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSanitizeDiscordResult(t *testing.T) {
	value := SanitizeDiscordResult("完成 /Volumes/workspace/private/file.go，token=ghp_abcdefghijklmnopqrstuvwxyz")
	require.Contains(t, value, "/Volumes/workspace/private/file.go")
	require.NotContains(t, value, "ghp_")
	require.Contains(t, value, "[已隐藏凭据]")

	long := SanitizeDiscordResult(strings.Repeat("你", 2100))
	require.Len(t, []rune(long), 2100)
	require.NotContains(t, long, "内容已截断")
}

func TestCodexErrorFallbackReadsLatestNonRetryableAgentEvent(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT event.payload FROM agent_events event")).
		WithArgs(runID).
		WillReturnRows(sqlmock.NewRows([]string{"payload"}).AddRow([]byte(
			`{"error":{"message":"at capacity","codexErrorInfo":"serverOverloaded","additionalDetails":"later"},"willRetry":false,"threadId":"thread-1","turnId":"turn-1"}`)))
	mock.ExpectClose()

	value := codexErrorFromStoredOrEvent(context.Background(), db, runID, nil)
	require.NotNil(t, value)
	require.Equal(t, "at capacity", value.Message)
	require.JSONEq(t, `"serverOverloaded"`, string(value.CodexErrorInfo))
	require.False(t, value.WillRetry)
	require.Equal(t, "thread-1", value.ThreadID)
	require.Equal(t, "turn-1", value.TurnID)
}

func TestSplitConversationReplyKeepsLinesAndMarkdownFences(t *testing.T) {
	line := strings.Repeat("内容", 40)
	content := strings.Repeat(line+"\n", 60)
	chunks := splitConversationReply(content, "100000000000000001",
		"100000000000000002", "")
	require.GreaterOrEqual(t, len(chunks), 3)
	for _, chunk := range chunks {
		for value := range strings.SplitSeq(chunk, "\n") {
			require.True(t, value == line || value == "", value)
		}
	}

	code := "```go\n" + strings.Repeat("fmt.Println(\"完整代码行\")\n", 180) + "```"
	codeChunks := splitConversationReply(code, "100000000000000001",
		"100000000000000002", "100000000000000003")
	require.Greater(t, len(codeChunks), 1)
	for _, chunk := range codeChunks {
		require.Zero(t, strings.Count(chunk, "```")%2)
	}
}

func TestSplitConversationReplyReservesBidirectionalLinks(t *testing.T) {
	content := strings.Repeat("没有换行但有句号。", 500)
	guildID, threadID := "100000000000000001", "100000000000000002"
	chunks := splitConversationReply(content, guildID, threadID, "100000000000000003")
	require.Greater(t, len(chunks), 2)
	for index, chunk := range chunks {
		rendered := chunk
		if index > 0 {
			rendered = replyPreviousLink(guildID, threadID, "100000000000000010") + rendered
		}
		if index < len(chunks)-1 {
			rendered += replyNextLink(guildID, threadID, "100000000000000011")
		}
		if index == 0 {
			rendered = "<@100000000000000003> " + rendered
		}
		require.LessOrEqual(t, len([]rune(rendered)), discordReplyMessageBudget)
		if index < len(chunks)-1 {
			require.True(t, strings.HasSuffix(chunk, "。"))
		}
	}
}

func TestConversationReplyMentionUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	conversationID := uuid.New()
	query := regexp.QuoteMeta("SELECT message.discord_user_id,")

	mock.ExpectQuery(query).WithArgs(conversationID, "message-1").
		WillReturnRows(sqlmock.NewRows([]string{"discord_user_id", "multiplayer"}).
			AddRow("100000000000000001", true))
	userID, err := conversationReplyMentionUser(context.Background(), db, conversationID, "message-1")
	require.NoError(t, err)
	require.Equal(t, "100000000000000001", userID)

	mock.ExpectQuery(query).WithArgs(conversationID, "message-2").
		WillReturnRows(sqlmock.NewRows([]string{"discord_user_id", "multiplayer"}).
			AddRow("100000000000000002", false))
	userID, err = conversationReplyMentionUser(context.Background(), db, conversationID, "message-2")
	require.NoError(t, err)
	require.Empty(t, userID)

	mock.ExpectQuery(query).WithArgs(conversationID, "message-3").
		WillReturnRows(sqlmock.NewRows([]string{"discord_user_id", "multiplayer"}).
			AddRow("invalid", true))
	userID, err = conversationReplyMentionUser(context.Background(), db, conversationID, "message-3")
	require.NoError(t, err)
	require.Empty(t, userID)

	mock.ExpectQuery(query).WithArgs(conversationID, "missing").
		WillReturnError(sql.ErrNoRows)
	userID, err = conversationReplyMentionUser(context.Background(), db, conversationID, "missing")
	require.NoError(t, err)
	require.Empty(t, userID)
	mock.ExpectClose()
}

func TestConversationReplyPayloadMentionsOnlyExplicitUser(t *testing.T) {
	payload := conversationReplyPayload("thread-1", "完成", "100000000000000001")
	require.Equal(t, "<@100000000000000001> 完成", payload["content"])
	require.Equal(t, []string{"100000000000000001"}, payload["mentionUserIds"])

	payload = conversationReplyPayload("thread-1", "完成", "")
	require.Equal(t, "完成", payload["content"])
	require.NotContains(t, payload, "mentionUserIds")
}

func TestConversationReplyModeUsesRunSnapshot(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	conversationID, runID := uuid.New(), uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT conversation.guild_id, run.collaboration_mode")).
		WithArgs(runID, conversationID, "thread-1").
		WillReturnRows(sqlmock.NewRows([]string{"guild_id", "collaboration_mode"}).
			AddRow("guild-1", "plan"))
	guildID, mode, err := conversationReplyMode(context.Background(), db,
		conversationID, "thread-1", runID)
	require.NoError(t, err)
	require.Equal(t, "guild-1", guildID)
	require.Equal(t, "plan", mode)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT guild_id, collaboration_mode")).
		WithArgs(conversationID, "thread-1").
		WillReturnRows(sqlmock.NewRows([]string{"guild_id", "collaboration_mode"}).
			AddRow("guild-1", "default"))
	guildID, mode, err = conversationReplyMode(context.Background(), db,
		conversationID, "thread-1", uuid.Nil)
	require.NoError(t, err)
	require.Equal(t, "guild-1", guildID)
	require.Equal(t, "default", mode)
	mock.ExpectClose()
}

func TestExpireConversationPlanCardsPropagatesDatabaseErrors(t *testing.T) {
	conversationID := uuid.New()
	startedRunID := uuid.New()
	databaseError := errors.New("database error")
	newMock := func(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		mock.MatchExpectationsInOrder(false)
		mock.ExpectClose()
		t.Cleanup(func() {
			require.NoError(t, db.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
		return db, mock
	}
	expectConversation := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT guild_id, thread_id FROM discord_conversations")).
			WithArgs(conversationID).
			WillReturnRows(sqlmock.NewRows([]string{"guild_id", "thread_id"}).
				AddRow("guild-1", "thread-1"))
	}

	t.Run("begin", func(t *testing.T) {
		db, mock := newMock(t)
		mock.ExpectBegin().WillReturnError(databaseError)
		require.ErrorIs(t, ExpireConversationPlanCards(context.Background(), db,
			conversationID, startedRunID), databaseError)
	})

	t.Run("conversation", func(t *testing.T) {
		db, mock := newMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT guild_id, thread_id FROM discord_conversations")).
			WithArgs(conversationID).WillReturnError(databaseError)
		mock.ExpectRollback()
		require.ErrorIs(t, ExpireConversationPlanCards(context.Background(), db,
			conversationID, startedRunID), databaseError)
	})

	t.Run("projections", func(t *testing.T) {
		db, mock := newMock(t)
		mock.ExpectBegin()
		expectConversation(mock)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT projection_key, COALESCE(message_id,'')")).
			WillReturnError(databaseError)
		mock.ExpectRollback()
		require.ErrorIs(t, ExpireConversationPlanCards(context.Background(), db,
			conversationID, startedRunID), databaseError)
	})

	t.Run("scan", func(t *testing.T) {
		db, mock := newMock(t)
		mock.ExpectBegin()
		expectConversation(mock)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT projection_key, COALESCE(message_id,'')")).
			WillReturnRows(sqlmock.NewRows([]string{"projection_key"}).AddRow("plan-key"))
		mock.ExpectRollback()
		require.Error(t, ExpireConversationPlanCards(context.Background(), db,
			conversationID, startedRunID))
	})

	t.Run("enqueue", func(t *testing.T) {
		db, mock := newMock(t)
		mock.ExpectBegin()
		expectConversation(mock)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT projection_key, COALESCE(message_id,'')")).
			WillReturnRows(sqlmock.NewRows([]string{"projection_key", "message_id"}).
				AddRow("plan-key", "message-1"))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO integration_outbox")).
			WillReturnError(databaseError)
		mock.ExpectRollback()
		require.ErrorIs(t, ExpireConversationPlanCards(context.Background(), db,
			conversationID, startedRunID), databaseError)
	})

	t.Run("delete", func(t *testing.T) {
		db, mock := newMock(t)
		mock.ExpectBegin()
		expectConversation(mock)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT projection_key, COALESCE(message_id,'')")).
			WillReturnRows(sqlmock.NewRows([]string{"projection_key", "message_id"}).
				AddRow("plan-key", ""))
		mock.ExpectExec(regexp.QuoteMeta("DELETE FROM discord_projections")).
			WillReturnError(databaseError)
		mock.ExpectRollback()
		require.ErrorIs(t, ExpireConversationPlanCards(context.Background(), db,
			conversationID, startedRunID), databaseError)
	})
}

func TestPlanExecutionCards(t *testing.T) {
	runID := uuid.New()
	completed := planCompletedCard(runID)
	require.Equal(t, "📋 Codex · Plan 已完成", completed.Header)
	require.Len(t, completed.Buttons, 1)
	require.Equal(t, planExecuteButtonPrefix+runID.String(), completed.Buttons[0].CustomID)

	started := planExecutionStartedCard()
	require.Equal(t, "✅ Codex · 已开始执行", started.Header)
	require.Empty(t, started.Buttons)
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT collaboration_mode FROM discord_conversations")).
		WithArgs(conversationID).
		WillReturnRows(sqlmock.NewRows([]string{"collaboration_mode"}).AddRow("default"))
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
