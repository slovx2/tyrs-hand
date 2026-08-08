package discordintegration

import (
	"context"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/google/uuid"
)

func (c *DisgoConnector) executePlanComponent(event *events.ComponentInteractionCreate,
	customID string,
) {
	runID, err := uuid.Parse(strings.TrimPrefix(customID, planExecuteButtonPrefix))
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().
			WithContent("这个执行计划按钮无效。").WithEphemeral(true))
		return
	}
	service := c.conversations
	if service == nil {
		service = NewConversationService(c.manager.db)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = service.ExecutePlan(ctx, c.guildID, event.Channel().ID().String(),
		event.User().ID.String(), event.User().EffectiveName(), event.User().Username,
		runID, event.ID().String())
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().
			WithContent(err.Error()).WithEphemeral(true))
		return
	}
	components, err := discordCardComponents(planExecutionStartedCard())
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().
			WithContent("计划已经开始执行，但操作卡暂时无法刷新。").WithEphemeral(true))
		return
	}
	update := discord.NewMessageUpdateV2(components...)
	emptyContent := ""
	emptyEmbeds := []discord.Embed{}
	update.Content, update.Embeds = &emptyContent, &emptyEmbeds
	update.AllowedMentions = &discord.AllowedMentions{}
	_ = event.UpdateMessage(update)
}
