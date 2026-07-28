package discordintegration

import (
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestModeButtonRevisionAndCard(t *testing.T) {
	id := uuid.New()
	state := ConversationModeState{ConversationID: id, Mode: "plan", Revision: 7,
		TriggerMode: "interactive", TriggerRevision: 3}
	card := conversationModeCard(state, "")
	require.Contains(t, card.Body, "`Plan`")
	require.Contains(t, card.Body, "`交互模式`")
	require.True(t, card.Buttons[0].Disabled)
	require.False(t, card.Buttons[1].Disabled)
	require.True(t, card.Buttons[2].Disabled)
	require.False(t, card.Buttons[3].Disabled)

	parsedID, revision, target, err := parseModeButton(card.Buttons[3].CustomID)
	require.NoError(t, err)
	require.Equal(t, id, parsedID)
	require.EqualValues(t, 7, revision)
	require.Equal(t, "default", target)
	require.Error(t, func() error {
		_, _, _, parseErr := parseModeButton("codex-mode:bad")
		return parseErr
	}())
	for _, customID := range []string{
		"codex-mode:not-a-uuid:1:plan",
		"codex-mode:" + id.String() + ":bad:plan",
		"codex-mode:" + id.String() + ":-1:plan",
		"codex-mode:" + id.String() + ":1:invalid",
	} {
		_, _, _, err = parseModeButton(customID)
		require.Error(t, err)
	}
	parsedID, revision, target, err = parseTriggerModeButton(card.Buttons[1].CustomID)
	require.NoError(t, err)
	require.Equal(t, id, parsedID)
	require.EqualValues(t, 3, revision)
	require.Equal(t, "discussion", target)
	for _, customID := range []string{
		"codex-trigger-mode:not-a-uuid:1:discussion",
		"codex-trigger-mode:" + id.String() + ":bad:discussion",
		"codex-trigger-mode:" + id.String() + ":-1:discussion",
		"codex-trigger-mode:" + id.String() + ":1:invalid",
	} {
		_, _, _, err = parseTriggerModeButton(customID)
		require.Error(t, err)
	}

	state.Busy = true
	state.TriggerMode = "discussion"
	card = conversationModeCard(state, "状态已刷新。")
	require.Contains(t, card.Body, "状态已刷新。")
	require.Contains(t, card.Body, "`讨论模式`")
	require.Contains(t, card.Body, "直接 @ Codex")
	require.False(t, card.Buttons[0].Disabled)
	require.True(t, card.Buttons[1].Disabled)
	require.True(t, card.Buttons[2].Disabled)
	require.True(t, card.Buttons[3].Disabled)
}

func TestMessageMentionsUserUsesStructuredMentions(t *testing.T) {
	botID := snowflake.ID(900)
	message := discord.Message{Content: "纯文本 @Codex 和 @everyone"}
	require.False(t, messageMentionsUser(message, botID))
	message.Mentions = []discord.User{{ID: snowflake.ID(901)}}
	require.False(t, messageMentionsUser(message, botID))
	message.Mentions = append(message.Mentions, discord.User{ID: botID})
	require.True(t, messageMentionsUser(message, botID))
	require.Zero(t, connectorUserID(nil))
	require.Equal(t, botID, connectorUserID(&bot.Client{ApplicationID: botID}))
}

func TestModeCommandResponseIsPublic(t *testing.T) {
	require.False(t, commandResponseEphemeral("/codex/mode"))
	require.True(t, commandResponseEphemeral("/codex/stop"))
}

func TestProgressCardOnlyShowsPlanMode(t *testing.T) {
	timeline := ConversationTimeline{Pages: []string{"处理中"}, Duration: time.Second, Updates: 1}
	plan := conversationProgressCard(ConversationRunning, timeline, 0, "", "plan")
	require.Contains(t, plan.Body, "模式：Plan")
	defaultMode := conversationProgressCard(ConversationRunning, timeline, 0, "", "default")
	require.NotContains(t, defaultMode.Body, "模式")

	waiting := interactiveCard(InteractiveProjection{Status: "pending", CollaborationMode: "plan",
		Questions: []InteractiveQuestion{{ID: "q", Header: "确认", Question: "继续？"}}})
	require.Contains(t, waiting.Body, "模式：Plan")
}

func TestCommandInteractionUpdateSetsComponentsV2Flag(t *testing.T) {
	components, err := discordCardComponents(conversationModeCard(ConversationModeState{
		ConversationID: uuid.New(), Mode: "plan", Revision: 1,
	}, ""))
	require.NoError(t, err)

	update := commandInteractionUpdate("", components)
	require.NotNil(t, update.Flags)
	require.True(t, update.Flags.Has(discord.MessageFlagIsComponentsV2))
	require.NotNil(t, update.Components)

	plain := commandInteractionUpdate("操作完成", nil)
	require.Nil(t, plain.Flags)
	require.Equal(t, "操作完成", *plain.Content)
}
