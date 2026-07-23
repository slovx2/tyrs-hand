package discordintegration

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const desktopInputPageRunes = 3500

// DesktopInputCards 把 Desktop 用户输入转换为稳定分页的 Discord 身份卡片。
func DesktopInputCards(displayName, input string) []ComponentCardPayload {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = "Desktop"
	}
	input = strings.TrimSpace(input)
	if input == "" {
		input = "（无文本输入）"
	}
	runes := []rune(input)
	pageCount := (len(runes) + desktopInputPageRunes - 1) / desktopInputPageRunes
	cards := make([]ComponentCardPayload, 0, pageCount)
	for page := 0; page < pageCount; page++ {
		start := page * desktopInputPageRunes
		end := min(start+desktopInputPageRunes, len(runes))
		card := ComponentCardPayload{
			AccentColor: 0x5865F2,
			Header:      "## 🖥️ " + displayName + " · Desktop",
			Body:        string(runes[start:end]),
		}
		if pageCount > 1 {
			card.Footer = fmt.Sprintf("第 %d/%d 页", page+1, pageCount)
		}
		cards = append(cards, card)
	}
	return cards
}

type desktopInputExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// EnqueueDesktopInputPages 用确定性 key 投影 Desktop 输入；startPage 用于跳过 Forum Starter。
func EnqueueDesktopInputPages(ctx context.Context, execer desktopInputExecer, threadID string,
	conversationID uuid.UUID, projectionKey, displayName, input string, startPage int,
) error {
	cards := DesktopInputCards(displayName, input)
	if startPage < 0 {
		startPage = 0
	}
	for page := startPage; page < len(cards); page++ {
		key := fmt.Sprintf("desktop-input:%s:%s:%d", conversationID, projectionKey, page)
		if err := enqueueDiscordOutbox(ctx, execer, key, "message.create",
			"channels/"+threadID+"/messages", map[string]any{
				"channelId": threadID, "card": cards[page],
			}, key); err != nil {
			return err
		}
	}
	return nil
}
