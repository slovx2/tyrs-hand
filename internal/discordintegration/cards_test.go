package discordintegration

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestConversationCardsKeepSystemAndReplyVisuallyDistinct(t *testing.T) {
	running := conversationProgressCard(ConversationRunning, "working")
	require.Equal(t, "⚙️ Codex · 处理中", running.Title)
	require.Equal(t, cardColorBlurple, running.Color)
	require.Contains(t, running.Footer, "此卡片")

	completed := conversationProgressCard(ConversationCompleted, "done")
	require.Equal(t, cardColorGreen, completed.Color)
	require.Contains(t, completed.Footer, "下一条消息")

	canceled := conversationProgressCard(ConversationCanceled, "stopped")
	require.Equal(t, cardColorGray, canceled.Color)
	require.Contains(t, canceled.Title, "已停止")
	failed := conversationProgressCard(ConversationFailed, "failed")
	require.Equal(t, cardColorRed, failed.Color)
	require.Contains(t, terminatedControlCard().Description, "没有进入执行队列")
}

func TestCardsSanitizeUntrustedContentAndRespectLimits(t *testing.T) {
	value := cardText("/Volumes/workspace/private ghp_abcdefghijklmnopqrstuvwxyz", 256)
	require.NotContains(t, value, "/Volumes/workspace")
	require.NotContains(t, value, "ghp_")
	require.Equal(t, 10, utf8.RuneCountInString(cardText(strings.Repeat("你", 20), 10)))

	card := taskCard(taskProjection{Kind: "pull_request", Number: 8, Title: strings.Repeat("很长", 200),
		Owner: "owner", Repository: "repo", WorkItemState: "open"}, "Running")
	require.LessOrEqual(t, utf8.RuneCountInString(card.Title), 256)
	require.Equal(t, "Pull Request", strings.Split(card.Title, " #")[0])
	require.Equal(t, cardColorBlurple, card.Color)
	require.Len(t, card.Fields, 3)
}

func TestDiscordEmbedValidationCoversDiscordLimits(t *testing.T) {
	valid := EmbedPayload{Title: "Status", Description: "Healthy", Color: cardColorGreen,
		Footer: "Updated", Timestamp: "2026-07-19T00:00:00Z",
		Fields: []EmbedFieldPayload{{Name: "Worker", Value: "1", Inline: true}}}
	embeds, err := discordEmbeds([]EmbedPayload{valid})
	require.NoError(t, err)
	require.Len(t, embeds, 1)
	require.Len(t, embeds[0].Fields, 1)
	require.NotNil(t, embeds[0].Timestamp)

	cases := []EmbedPayload{
		{},
		{Title: strings.Repeat("x", 257)},
		{Color: 0x1000000},
		{Timestamp: "yesterday"},
		{Fields: []EmbedFieldPayload{{Name: "", Value: "value"}}},
		{Description: strings.Repeat("x", 4096), Footer: strings.Repeat("y", 1905)},
	}
	for _, value := range cases {
		_, err := discordEmbeds([]EmbedPayload{value})
		require.Error(t, err)
	}
	_, err = discordEmbeds(make([]EmbedPayload, 11))
	require.Error(t, err)
	_, err = discordEmbeds([]EmbedPayload{{Description: strings.Repeat("x", 3001)}, {Description: strings.Repeat("y", 3000)}})
	require.ErrorContains(t, err, "总长度")
}

func TestSystemCardSeverity(t *testing.T) {
	require.Equal(t, cardColorGreen, systemStatusCard(0, 1, 0, 1, 0, "connected").Color)
	require.Equal(t, cardColorYellow, systemStatusCard(0, 1, 1, 1, 0, "connected").Color)
	require.Equal(t, cardColorRed, systemStatusCard(0, 0, 0, 0, 0, "disconnected").Color)
	require.Equal(t, cardColorGreen, systemAlertsCard("connected", false, 1, 0).Color)
	require.Equal(t, cardColorRed, systemAlertsCard("disconnected", true, 0, 2).Color)
}
