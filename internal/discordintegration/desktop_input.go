package discordintegration

import (
	"fmt"
	"strings"
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
			Header:      "🖥️ " + displayName + " · Desktop",
			Body:        string(runes[start:end]),
		}
		if pageCount > 1 {
			card.Header += fmt.Sprintf(" · %d/%d", page+1, pageCount)
		}
		cards = append(cards, card)
	}
	return cards
}
