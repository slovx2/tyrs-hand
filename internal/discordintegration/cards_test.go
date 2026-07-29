package discordintegration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestConversationCardsKeepSystemAndReplyVisuallyDistinct(t *testing.T) {
	timeline := ConversationTimeline{Pages: []string{"working"}, Updates: 2, Duration: 3 * time.Second}
	running := conversationProgressCard(ConversationRunning, timeline, 0, "", "default")
	require.Contains(t, running.Header, "思考中")
	require.Contains(t, running.Body, "2 项动态")
	require.NotContains(t, running.Body, "条更新")
	require.Equal(t, cardColorBlurple, running.AccentColor)

	completed := conversationProgressCard(ConversationCompleted, timeline, 0, "", "default")
	require.Equal(t, cardColorGreen, completed.AccentColor)
	canceled := conversationProgressCard(ConversationCanceled, timeline, 0, "", "default")
	require.Equal(t, cardColorGray, canceled.AccentColor)
	require.Contains(t, canceled.Header, "已停止")
	failed := conversationProgressCard(ConversationFailed, timeline, 0, "", "default")
	require.Equal(t, cardColorRed, failed.AccentColor)
	require.Contains(t, terminatedControlCard().Body, "没有进入执行队列")
}

func TestConversationCardPaginationKeepsStatusAndUsesUniqueButtons(t *testing.T) {
	runID := "00000000-0000-0000-0000-000000000001"
	timeline := ConversationTimeline{Pages: []string{"older", "newer", "latest"}, Updates: 7,
		Duration: time.Minute}
	card := conversationProgressCard(ConversationCompleted, timeline, 1, runID, "default")
	require.Contains(t, card.Header, "已完成")
	require.Equal(t, "newer", card.Timeline)
	require.Len(t, card.Buttons, 4)
	require.Equal(t, "2 / 3", card.Buttons[1].Label)
	seen := map[string]bool{}
	for _, button := range card.Buttons {
		require.False(t, seen[button.CustomID])
		seen[button.CustomID] = true
	}
	require.Contains(t, card.Buttons[0].CustomID, "older")
	require.Contains(t, card.Buttons[2].CustomID, "newer")
	require.Contains(t, card.Buttons[3].CustomID, "latest")
}

func TestCardsSanitizeUntrustedContentAndRespectLimits(t *testing.T) {
	value := cardText("/Volumes/workspace/private ghp_abcdefghijklmnopqrstuvwxyz", 256)
	require.Contains(t, value, "/Volumes/workspace/private")
	require.NotContains(t, value, "ghp_")
	require.Equal(t, 10, utf8.RuneCountInString(cardText(strings.Repeat("你", 20), 10)))

	card := taskCard(taskProjection{Kind: "pull_request", Number: 8, Title: strings.Repeat("很长", 200),
		Owner: "owner", Repository: "repo", WorkItemState: "open"}, "Running")
	require.LessOrEqual(t, utf8.RuneCountInString(strings.TrimPrefix(card.Header, "## ")), 256)
	require.Contains(t, card.Header, "Pull Request #8")
	require.Equal(t, cardColorBlurple, card.AccentColor)
	require.Contains(t, card.Body, "owner/repo")
}

func TestDiscordComponentsV2CardStructureAndLimits(t *testing.T) {
	card := ComponentCardPayload{AccentColor: cardColorGreen, Header: "## Status", Body: "Healthy",
		Timeline: "过程", Buttons: []ComponentButtonPayload{
			{Label: "继续", CustomID: "continue", Style: "primary"},
		}}
	components, err := discordCardComponents(card)
	require.NoError(t, err)
	require.Len(t, components, 1)
	container, ok := components[0].(discord.ContainerComponent)
	require.True(t, ok)
	require.Equal(t, cardColorGreen, container.AccentColor)
	require.IsType(t, discord.TextDisplayComponent{}, container.Components[0])
	require.IsType(t, discord.SeparatorComponent{}, container.Components[1])
	require.IsType(t, discord.ActionRowComponent{}, container.Components[len(container.Components)-1])

	encoded, err := json.Marshal(discord.NewMessageCreateV2(components...))
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"flags":32768`)
	require.NotContains(t, string(encoded), `"embeds"`)

	_, err = discordCardComponents(ComponentCardPayload{AccentColor: 0x1000000, Header: "x"})
	require.Error(t, err)
	_, err = discordCardComponents(ComponentCardPayload{Header: ""})
	require.Error(t, err)
	_, err = discordCardComponents(ComponentCardPayload{Header: "x", Timeline: strings.Repeat("你", 4001)})
	require.ErrorContains(t, err, "4000")
	duplicate := ComponentCardPayload{Header: "x", Buttons: []ComponentButtonPayload{
		{Label: "a", CustomID: "same"}, {Label: "b", CustomID: "same"},
	}}
	_, err = discordCardComponents(duplicate)
	require.ErrorContains(t, err, "重复")
	link := guidedConversationCard("https://discord.com/channels/1/2/3")
	require.Equal(t, "Codex · 已引导对话", link.Header)
	require.Empty(t, link.Body)
	require.Empty(t, link.Timeline)
	require.Equal(t, "查看最新状态", link.Buttons[0].Label)
	_, err = discordCardComponents(link)
	require.NoError(t, err)
	_, err = discordCardComponents(ComponentCardPayload{Header: "x", Buttons: []ComponentButtonPayload{{
		Label: "invalid", CustomID: "id", URL: "https://discord.com",
	}}})
	require.Error(t, err)
	_, err = discordCardComponents(ComponentCardPayload{Header: "x", Buttons: []ComponentButtonPayload{{
		Label: "invalid", URL: "javascript:alert(1)",
	}}})
	require.Error(t, err)
}

func TestDiscordComponentsV2SupportsMultipleButtonRows(t *testing.T) {
	card := ComponentCardPayload{AccentColor: cardColorYellow, Header: "settings",
		Buttons: []ComponentButtonPayload{{Label: "first", CustomID: "first"}},
		ButtonRows: [][]ComponentButtonPayload{{{
			Label: "start", CustomID: "start", Style: "primary",
		}}}}
	components, err := discordCardComponents(card)
	require.NoError(t, err)
	container := components[0].(discord.ContainerComponent)
	actionRows := 0
	for _, component := range container.Components {
		if _, ok := component.(discord.ActionRowComponent); ok {
			actionRows++
		}
	}
	require.Equal(t, 2, actionRows)
}

func TestConversationCardsOmitInternalPlaceholderDetails(t *testing.T) {
	tracker := NewConversationActionTracker(time.Now())
	for _, detail := range []string{"正在处理请求。", "思考中", "本轮处理完成。",
		"本轮已由 Discord 用户主动停止。", "本轮处理未完成。",
		"Codex Desktop 正在处理请求。"} {
		timeline := tracker.Timeline(detail, time.Second)
		require.Empty(t, timeline.Pages, detail)
		card := conversationProgressCard(ConversationRunning, timeline, 0, "", "default")
		require.Empty(t, card.Timeline, detail)
	}
	require.Equal(t, []string{"参数已确认"}, tracker.Timeline("参数已确认", time.Second).Pages)
}

func TestSystemCardSeverity(t *testing.T) {
	require.Equal(t, cardColorGreen, systemStatusCard(0, 1, 0, 1, 0, "connected").AccentColor)
	require.Equal(t, cardColorYellow, systemStatusCard(0, 1, 1, 1, 0, "connected").AccentColor)
	require.Equal(t, cardColorRed, systemStatusCard(0, 0, 0, 0, 0, "disconnected").AccentColor)
	require.Equal(t, cardColorGreen, systemAlertsCard("connected", false, 1, 0).AccentColor)
	require.Equal(t, cardColorRed, systemAlertsCard("disconnected", true, 0, 2).AccentColor)
}

func TestEverySystemCardBuildsAsComponentsV2(t *testing.T) {
	timeline := ConversationTimeline{Pages: []string{"timeline"}, Duration: time.Second}
	cards := []ComponentCardPayload{
		conversationProgressCard(ConversationRunning, timeline, 0, "", "default"),
		conversationProgressCard(ConversationCompleted, timeline, 0, "", "default"),
		conversationProgressCard(ConversationCanceled, timeline, 0, "", "default"),
		conversationProgressCard(ConversationFailed, timeline, 0, "", "default"),
		terminatedControlCard(), conversationModeCard(ConversationModeState{ConversationID: uuid.New(),
			Mode: "default", TriggerMode: "interactive", Model: "gpt-5.6-sol",
			ReasoningEffort: "high", ServiceTier: "fast", Awaiting: true}, ""),
		archivedConversationCard(), lifecycleCard(uuid.New(), 1),
		DesktopInputCards("Avery", "hello")[0],
		interactiveCard(InteractiveProjection{Status: "pending", Questions: []InteractiveQuestion{{
			ID: "confirm", Header: "确认", Question: "继续吗？",
		}}}),
		taskCard(taskProjection{Kind: "issue", Number: 1, Title: "task", WorkItemState: "open"}, "Running"),
		taskStateChangeCard("Running", "Completed"),
		systemStatusCard(0, 1, 0, 1, 0, "connected"),
		systemAlertsCard("connected", false, 1, 0),
	}
	for _, card := range cards {
		require.False(t, strings.HasPrefix(strings.TrimSpace(card.Header), "#"), card.Header)
		components, err := discordCardComponents(card)
		require.NoError(t, err, card.Header)
		encoded, err := json.Marshal(discord.NewMessageCreateV2(components...))
		require.NoError(t, err)
		require.Contains(t, string(encoded), `"flags":32768`)
		require.NotContains(t, string(encoded), `"embeds"`)
	}
}
