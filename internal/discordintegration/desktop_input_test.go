package discordintegration

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDesktopInputCardsPreserveIdentityAndPaginateDeterministically(t *testing.T) {
	input := strings.Repeat("桌面输入", 1200)
	cards := DesktopInputCards("Kal", input)
	require.Greater(t, len(cards), 1)
	var rebuilt strings.Builder
	for index, card := range cards {
		require.Contains(t, card.Header, "Kal · Desktop")
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

func TestEnqueueDesktopInputPagesNormalizesStartAndSkipsExistingPages(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	conversationID := uuid.New()
	mock.ExpectExec("INSERT INTO integration_outbox").
		WillReturnResult(sqlmock.NewResult(1, 1))
	require.NoError(t, EnqueueDesktopInputPages(context.Background(), db, "thread-1",
		conversationID, "client-message-1", "Kal", "hello", -1))
	require.NoError(t, EnqueueDesktopInputPages(context.Background(), db, "thread-1",
		conversationID, "client-message-1", "Kal", "hello", 1),
		"Starter 已覆盖唯一一页时不应重复创建消息")
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}
