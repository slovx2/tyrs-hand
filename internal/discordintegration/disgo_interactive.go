package discordintegration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/google/uuid"
)

func (c *DisgoConnector) answerInteractiveComponent(event *events.ComponentInteractionCreate,
	customID string,
) {
	id, question, option, err := parseInteractiveButton(customID)
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent("这个回答按钮已失效。").WithEphemeral(true))
		return
	}
	if option < 0 {
		input := discord.NewParagraphTextInput("answer").WithRequired(true).WithMaxLength(2000)
		modal := discord.NewModalCreate(fmt.Sprintf("%s%s:%d", interactiveModalPrefix, id, question),
			"回答 Codex 问题", discord.NewLabel("你的回答", input))
		_ = event.Modal(modal)
		return
	}
	result, err := c.manager.AnswerInteractive(context.Background(), c.guildID, id,
		question, option, "")
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(err.Error()).WithEphemeral(true))
		return
	}
	card := result.Card
	if result.Complete {
		if err := ProjectInteractiveRequest(context.Background(), c.manager.db, id); err != nil {
			_ = event.CreateMessage(discord.NewMessageCreate().
				WithContent("回答已保存，但完整回答卡暂时无法投递。").WithEphemeral(true))
			return
		}
		card = interactiveSubmittedCard()
	}
	components, err := discordCardComponents(card)
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent("回答已保存，但卡片暂时无法更新。").WithEphemeral(true))
		return
	}
	update := discord.NewMessageUpdateV2(components...)
	emptyContent := ""
	emptyEmbeds := []discord.Embed{}
	update.Content, update.Embeds = &emptyContent, &emptyEmbeds
	update.AllowedMentions = &discord.AllowedMentions{}
	_ = event.UpdateMessage(update)
}

func (c *DisgoConnector) answerInteractiveModal(event *events.ModalSubmitInteractionCreate) {
	id, question, err := parseInteractiveModal(event.Data.CustomID)
	answer := strings.TrimSpace(event.Data.Text("answer"))
	var result InteractiveAnswerResult
	if err == nil && answer == "" {
		err = errors.New("回答不能为空")
	}
	if err == nil {
		result, err = c.manager.AnswerInteractive(context.Background(), c.guildID, id,
			question, -1, answer)
	}
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(err.Error()).WithEphemeral(true))
		return
	}
	if result.Complete {
		if err = ProjectInteractiveRequest(context.Background(), c.manager.db, id); err != nil {
			_ = event.CreateMessage(discord.NewMessageCreate().
				WithContent("回答已保存，但完整回答卡暂时无法投递。").WithEphemeral(true))
			return
		}
		result.Card = interactiveSubmittedCard()
	}
	components, err := discordCardComponents(result.Card)
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().
			WithContent("回答已保存，但卡片暂时无法更新。").WithEphemeral(true))
		return
	}
	update := discord.NewMessageUpdateV2(components...)
	emptyContent := ""
	emptyEmbeds := []discord.Embed{}
	update.Content, update.Embeds = &emptyContent, &emptyEmbeds
	update.AllowedMentions = &discord.AllowedMentions{}
	_ = event.UpdateMessage(update)
}

func parseInteractiveModal(value string) (uuid.UUID, int, error) {
	if !strings.HasPrefix(value, interactiveModalPrefix) {
		return uuid.Nil, 0, errors.New("交互回答窗口前缀无效")
	}
	parts := strings.Split(strings.TrimPrefix(value, interactiveModalPrefix), ":")
	if len(parts) != 2 {
		return uuid.Nil, 0, errors.New("交互回答窗口格式无效")
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		return uuid.Nil, 0, err
	}
	var question int
	if _, err = fmt.Sscanf(parts[1], "%d", &question); err != nil || question < 0 {
		return uuid.Nil, 0, errors.New("交互回答问题序号无效")
	}
	return id, question, nil
}
