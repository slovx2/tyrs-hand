package discordintegration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDesktopInputCardsPreserveIdentityAndPaginateDeterministically(t *testing.T) {
	input := strings.Repeat("桌面输入", 1200)
	cards := DesktopInputCards("Avery", input)
	require.Greater(t, len(cards), 1)
	var rebuilt strings.Builder
	for index, card := range cards {
		require.Contains(t, card.Header, "Avery · Desktop")
		require.LessOrEqual(t, len([]rune(card.Body)), desktopInputPageRunes)
		require.Contains(t, card.Header, "/")
		rebuilt.WriteString(card.Body)
		require.Contains(t, card.Header, fmt.Sprintf("%d/%d", index+1, len(cards)))
	}
	require.Equal(t, input, rebuilt.String())
}

func TestDesktopInputCardsUseStableFallbacksForEmptyIdentityAndText(t *testing.T) {
	cards := DesktopInputCards(" \n ", " \t ")
	require.Len(t, cards, 1)
	require.Contains(t, cards[0].Header, "Desktop · Desktop")
	require.Equal(t, "（无文本输入）", cards[0].Body)
}

func TestFormatDesktopProjectionInputHidesStructuredLocalPath(t *testing.T) {
	formatted := FormatDesktopProjectionInput("检查 /private/tmp/shot.png 后回复",
		json.RawMessage(`{"input":[{"type":"localImage","path":"/private/tmp/shot.png"}]}`),
		[]string{"broken.webp（读取失败）"})

	require.NotContains(t, formatted, "/private/tmp")
	require.Contains(t, formatted, "shot.png")
	require.Contains(t, formatted, "broken.webp（读取失败）")

	ordinary := FormatDesktopProjectionInput("保留 /private/tmp/shot.png",
		json.RawMessage(`{"input":[{"type":"text","path":"/private/tmp/shot.png"}]}`), nil)
	require.Contains(t, ordinary, "/private/tmp/shot.png")
}

func TestEnqueueDesktopInputPagesNormalizesStartAndSkipsExistingPages(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	conversationID := uuid.New()
	mock.ExpectQuery("SELECT guild_id").WithArgs(conversationID).
		WillReturnRows(sqlmock.NewRows([]string{"guild_id", "starter_message_id"}).
			AddRow("guild-1", "starter-1"))
	mock.ExpectQuery("INSERT INTO discord_projections").
		WillReturnRows(sqlmock.NewRows([]string{"message_id"}).AddRow(""))
	mock.ExpectExec("INSERT INTO integration_outbox").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT projection_key").
		WillReturnRows(sqlmock.NewRows([]string{"projection_key", "message_id"}).
			AddRow("desktop-input:"+conversationID.String()+":client-message-1:0", ""))
	require.NoError(t, EnqueueDesktopInputPages(context.Background(), db, "thread-1",
		conversationID, "client-message-1", "Avery", "hello", -1))
	mock.ExpectQuery("SELECT guild_id").WithArgs(conversationID).
		WillReturnRows(sqlmock.NewRows([]string{"guild_id", "starter_message_id"}).
			AddRow("guild-1", "starter-1"))
	mock.ExpectQuery("INSERT INTO discord_projections").
		WillReturnRows(sqlmock.NewRows([]string{"message_id"}).AddRow("starter-1"))
	mock.ExpectQuery("SELECT projection_key").
		WillReturnRows(sqlmock.NewRows([]string{"projection_key", "message_id"}).
			AddRow("desktop-input:"+conversationID.String()+":client-message-1:0", "starter-1"))
	require.NoError(t, EnqueueDesktopInputPages(context.Background(), db, "thread-1",
		conversationID, "client-message-1", "Avery", "hello", 1),
		"Starter 已覆盖唯一一页时不应重复创建消息")
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}
