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

func (c *DisgoConnector) answerOfficialComponent(event *events.ComponentInteractionCreate,
	customID string,
) {
	var result OfficialAnswerResult
	var id uuid.UUID
	var err error
	if strings.HasPrefix(customID, officialInputButtonPrefix) {
		var question, option int
		id, question, option, err = parseOfficialInputButton(customID)
		if err == nil && option < 0 {
			input := discord.NewParagraphTextInput("answer").WithRequired(true).WithMaxLength(2000)
			modal := discord.NewModalCreate(fmt.Sprintf("%s%s:%d", officialInputModalPrefix,
				id, question), "回答 Codex 问题", discord.NewLabel("你的回答", input))
			err = event.Modal(modal)
			if err != nil {
				_ = event.CreateMessage(discord.NewMessageCreate().WithContent(
					"暂时无法打开回答窗口，请重试。").WithEphemeral(true))
			}
			return
		}
		if err == nil {
			result, err = c.manager.AnswerOfficialInput(context.Background(), c.guildID,
				id, question, option, "")
		}
	} else {
		var decision string
		id, decision, err = parseOfficialApproval(customID)
		if err == nil {
			result, err = c.manager.AnswerOfficialApproval(context.Background(), c.guildID,
				id, decision)
		}
	}
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(err.Error()).WithEphemeral(true))
		return
	}
	_ = ProjectOfficialServerRequest(context.Background(), c.manager.db, id)
	c.updateOfficialInteractionMessage(event, result.Card)
}

func (c *DisgoConnector) answerOfficialModal(event *events.ModalSubmitInteractionCreate) {
	id, question, err := parseOfficialInputModal(event.Data.CustomID)
	answer := strings.TrimSpace(event.Data.Text("answer"))
	if err == nil && answer == "" {
		err = errors.New("回答不能为空")
	}
	var result OfficialAnswerResult
	if err == nil {
		result, err = c.manager.AnswerOfficialInput(context.Background(), c.guildID, id,
			question, -1, answer)
	}
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(err.Error()).WithEphemeral(true))
		return
	}
	_ = ProjectOfficialServerRequest(context.Background(), c.manager.db, id)
	components, err := discordCardComponents(result.Card)
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(
			"回答已保存，但卡片暂时无法更新。").WithEphemeral(true))
		return
	}
	update := discord.NewMessageUpdateV2(components...)
	emptyContent := ""
	emptyEmbeds := []discord.Embed{}
	update.Content, update.Embeds = &emptyContent, &emptyEmbeds
	update.AllowedMentions = &discord.AllowedMentions{}
	_ = event.UpdateMessage(update)
}

func (c *DisgoConnector) updateOfficialInteractionMessage(
	event *events.ComponentInteractionCreate, card ComponentCardPayload,
) {
	components, err := discordCardComponents(card)
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(
			"操作已保存，但卡片暂时无法更新。").WithEphemeral(true))
		return
	}
	update := discord.NewMessageUpdateV2(components...)
	emptyContent := ""
	emptyEmbeds := []discord.Embed{}
	update.Content, update.Embeds = &emptyContent, &emptyEmbeds
	update.AllowedMentions = &discord.AllowedMentions{}
	_ = event.UpdateMessage(update)
}

func parseOfficialApproval(value string) (uuid.UUID, string, error) {
	if !strings.HasPrefix(value, officialApprovalPrefix) {
		return uuid.Nil, "", errors.New("官方授权按钮无效")
	}
	parts := strings.Split(strings.TrimPrefix(value, officialApprovalPrefix), ":")
	if len(parts) != 2 {
		return uuid.Nil, "", errors.New("官方授权按钮无效")
	}
	id, err := uuid.Parse(parts[0])
	if err != nil || (parts[1] != "accept" && parts[1] != "decline" && parts[1] != "cancel") {
		return uuid.Nil, "", errors.New("官方授权按钮无效")
	}
	return id, parts[1], nil
}

func parseOfficialInputModal(value string) (uuid.UUID, int, error) {
	if !strings.HasPrefix(value, officialInputModalPrefix) {
		return uuid.Nil, 0, errors.New("官方回答窗口无效")
	}
	parts := strings.Split(strings.TrimPrefix(value, officialInputModalPrefix), ":")
	if len(parts) != 2 {
		return uuid.Nil, 0, errors.New("官方回答窗口无效")
	}
	id, err := uuid.Parse(parts[0])
	var question int
	if _, scanErr := fmt.Sscanf(parts[1], "%d", &question); err != nil || scanErr != nil ||
		question < 0 {
		return uuid.Nil, 0, errors.New("官方回答窗口无效")
	}
	return id, question, nil
}
