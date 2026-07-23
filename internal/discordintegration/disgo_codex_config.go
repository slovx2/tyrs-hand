package discordintegration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	disgorest "github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
)

const configurationModalPrefix = "codex-config-modal:"
const newCodexModalPrefix = "codex-new-modal:"

func (c *DisgoConnector) startConfiguredConversation(event *events.ComponentInteractionCreate, rawID string) {
	id, err := uuid.Parse(rawID)
	if err == nil {
		err = c.conversations.FinalizeConfiguration(context.Background(), id, event.User().ID.String(), nil)
	}
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(err.Error()).WithEphemeral(true))
		return
	}
	timeline := ConversationTimeline{Pages: []string{"已按默认参数启动，消息正在进入长期开发环境队列。"},
		Duration: time.Second}
	components, componentErr := discordCardComponents(conversationProgressCard(ConversationRunning,
		timeline, 0, ""))
	if componentErr != nil {
		return
	}
	update := discord.NewMessageUpdateV2(components...)
	emptyContent := ""
	emptyEmbeds := []discord.Embed{}
	update.Content, update.Embeds = &emptyContent, &emptyEmbeds
	update.AllowedMentions = &discord.AllowedMentions{}
	_ = event.UpdateMessage(update)
}

func (c *DisgoConnector) editConversationConfiguration(event *events.ComponentInteractionCreate, rawID string) {
	id, err := uuid.Parse(rawID)
	if err == nil {
		err = c.conversations.BeginConfigurationEdit(context.Background(), id, event.User().ID.String())
	}
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(err.Error()).WithEphemeral(true))
		return
	}
	modal, err := c.configurationModal(context.Background(), id)
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(err.Error()).WithEphemeral(true))
		return
	}
	_ = event.Modal(modal)
}

func (c *DisgoConnector) configurationModal(ctx context.Context, conversationID uuid.UUID) (discord.ModalCreate, error) {
	var model, effort, tier string
	err := c.manager.db.QueryRowContext(ctx, `SELECT COALESCE(model,''), COALESCE(reasoning_effort,''),
		COALESCE(service_tier,'standard')
		FROM discord_conversations WHERE id = $1`, conversationID).Scan(&model, &effort, &tier)
	if err != nil {
		return discord.ModalCreate{}, err
	}
	modelOptions := make([]discord.StringSelectMenuOption, 0, len(codexsettings.PresetModels)+2)
	preset := false
	for _, value := range codexsettings.PresetModels {
		selected := value == model
		preset = preset || selected
		modelOptions = append(modelOptions, discord.NewStringSelectMenuOption(value, value).WithDefault(selected))
	}
	modelOptions = append(modelOptions,
		discord.NewStringSelectMenuOption("Codex 默认", "__default__").WithDefault(model == ""),
		discord.NewStringSelectMenuOption("自定义", "__custom__").WithDefault(model != "" && !preset))
	custom := discord.NewShortTextInput("custom_model").WithRequired(false).WithMaxLength(128)
	if model != "" && !preset {
		custom = custom.WithValue(model)
	}
	modelSelect := discord.NewStringSelectMenu("model", "选择模型", modelOptions...).WithRequired(true)
	tierSelect := discord.NewStringSelectMenu("service_tier", "选择服务等级",
		discord.NewStringSelectMenuOption("标准", "standard").WithDefault(tier != "fast"),
		discord.NewStringSelectMenuOption("快速", "fast").WithDefault(tier == "fast")).WithRequired(true)
	effortSelect := discord.NewStringSelectMenu("reasoning_effort", "选择思考等级",
		discord.NewStringSelectMenuOption("Codex 默认", "__default__").WithDefault(effort == ""),
		discord.NewStringSelectMenuOption("轻", "low").WithDefault(effort == "low"),
		discord.NewStringSelectMenuOption("中", "medium").WithDefault(effort == "medium"),
		discord.NewStringSelectMenuOption("高", "high").WithDefault(effort == "high"),
		discord.NewStringSelectMenuOption("极高", "xhigh").WithDefault(effort == "xhigh")).WithRequired(true)
	return discord.NewModalCreate(configurationModalPrefix+conversationID.String(), "调整本次 Codex 参数",
		discord.NewLabel("模型", modelSelect), discord.NewLabel("自定义模型", custom),
		discord.NewLabel("服务等级", tierSelect), discord.NewLabel("思考等级", effortSelect)), nil
}

func (c *DisgoConnector) onModalSubmit(event *events.ModalSubmitInteractionCreate) {
	if event.GuildID() == nil || event.GuildID().String() != c.guildID {
		return
	}
	eventID := "interaction:" + event.ID().String()
	inserted, err := c.manager.RecordInboundEvent(context.Background(), eventID, c.guildID,
		"MODAL_SUBMIT", map[string]string{"id": event.ID().String(), "customId": event.Data.CustomID})
	if err != nil || !inserted {
		return
	}
	defer func() { _ = c.manager.CompleteInboundEvent(context.Background(), eventID, nil) }()
	if strings.HasPrefix(event.Data.CustomID, interactiveModalPrefix) {
		c.answerInteractiveModal(event)
		return
	}
	if strings.HasPrefix(event.Data.CustomID, newCodexModalPrefix) {
		c.createCodexPost(event)
		return
	}
	if !strings.HasPrefix(event.Data.CustomID, configurationModalPrefix) {
		return
	}
	id, err := uuid.Parse(strings.TrimPrefix(event.Data.CustomID, configurationModalPrefix))
	model := firstModalValue(event.Data.StringValues("model"))
	custom := strings.TrimSpace(event.Data.Text("custom_model"))
	customSelected := model == "__custom__"
	switch model {
	case "__custom__":
		model = custom
	case "__default__":
		model = ""
	}
	effort := firstModalValue(event.Data.StringValues("reasoning_effort"))
	if effort == "__default__" {
		effort = ""
	}
	tier := firstModalValue(event.Data.StringValues("service_tier"))
	if err == nil && customSelected && model == "" {
		err = errors.New("选择自定义模型时必须填写模型名称")
	}
	if err == nil {
		err = c.conversations.FinalizeConfiguration(context.Background(), id, event.User().ID.String(),
			&ConversationConfiguration{Model: model, ReasoningEffort: effort, ServiceTier: tier})
	}
	message := "配置已保存，Codex 会话开始运行。"
	if err != nil {
		message = err.Error()
	}
	_ = event.CreateMessage(discord.NewMessageCreate().WithContent(message).WithEphemeral(true))
	if err == nil {
		var guildID, threadID, starterID string
		queryErr := c.manager.db.QueryRowContext(context.Background(), `SELECT guild_id, thread_id,
			COALESCE(starter_message_id,'') FROM discord_conversations WHERE id = $1`, id).
			Scan(&guildID, &threadID, &starterID)
		if queryErr == nil {
			_ = ProjectConversationStatus(context.Background(), c.manager.db, guildID, threadID, id,
				starterID, uuid.Nil, ConversationRunning, "参数已确认，消息正在进入长期开发环境队列。")
		}
	}
}

func (c *DisgoConnector) showForumSelector(event *events.ComponentInteractionCreate) {
	selector := discord.NewChannelSelectMenu("codex-new-forum", "选择开发 Forum").
		WithChannelTypes(discord.ChannelTypeGuildForum).WithRequired(true)
	message := discord.NewMessageCreate().WithContent("选择要创建 Codex 帖子的开发 Forum：").
		WithComponents(discord.NewActionRow(selector)).WithEphemeral(true)
	_ = event.CreateMessage(message)
}

func (c *DisgoConnector) newCodexModal(ctx context.Context, forumDiscordID, userID string) (discord.ModalCreate, error) {
	forumID, repositoryID, profileID, err := c.authorizedForum(ctx, forumDiscordID, userID)
	if err != nil {
		return discord.ModalCreate{}, err
	}
	preferences, err := codexsettings.NewService(c.manager.db).Resolve(ctx, repositoryID, forumID, profileID)
	if err != nil {
		return discord.ModalCreate{}, err
	}
	modelOptions, custom := modelModalOptions(preferences.Model)
	modelSelect := discord.NewStringSelectMenu("model", "选择模型", modelOptions...).WithRequired(true)
	tierSelect := discord.NewStringSelectMenu("service_tier", "选择服务等级",
		discord.NewStringSelectMenuOption("标准", "standard").WithDefault(preferences.ServiceTier != "fast"),
		discord.NewStringSelectMenuOption("快速", "fast").WithDefault(preferences.ServiceTier == "fast")).WithRequired(true)
	effortSelect := effortModalSelect(preferences.ReasoningEffort)
	task := discord.NewParagraphTextInput("task").WithRequired(true).WithMinLength(1).WithMaxLength(2000).
		WithPlaceholder("描述希望 Codex 完成的任务")
	return discord.NewModalCreate(newCodexModalPrefix+forumDiscordID, "新建 Codex 帖子",
		discord.NewLabel("任务", task), discord.NewLabel("模型", modelSelect),
		discord.NewLabel("自定义模型", custom), discord.NewLabel("服务等级", tierSelect),
		discord.NewLabel("思考等级", effortSelect)), nil
}

func (c *DisgoConnector) authorizedForum(ctx context.Context, forumDiscordID, userID string) (
	uuid.UUID, uuid.UUID, uuid.UUID, error,
) {
	var forumID, repositoryID, profileID uuid.UUID
	var owner string
	err := c.manager.db.QueryRowContext(ctx, `SELECT f.id, f.repository_id, f.owner_discord_user_id,
		(SELECT id FROM agent_profiles ORDER BY created_at LIMIT 1)
		FROM discord_forums f JOIN discord_resources r ON r.id = f.resource_id
		WHERE f.guild_id = $1 AND r.discord_id = $2 AND f.forum_type = 'development'`,
		c.guildID, forumDiscordID).Scan(&forumID, &repositoryID, &owner, &profileID)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, errors.New("所选频道不是可用的开发 Forum")
	}
	if userID != owner {
		var operator bool
		err = c.manager.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM discord_forum_access
			WHERE forum_id = $1 AND discord_user_id = $2 AND access_level = 'operator')`, forumID, userID).
			Scan(&operator)
		if err != nil || !operator {
			return uuid.Nil, uuid.Nil, uuid.Nil, errors.New("当前用户没有在该 Forum 新建 Codex 会话的权限")
		}
	}
	return forumID, repositoryID, profileID, nil
}

func modelModalOptions(model string) ([]discord.StringSelectMenuOption, discord.TextInputComponent) {
	options := make([]discord.StringSelectMenuOption, 0, len(codexsettings.PresetModels)+2)
	preset := false
	for _, value := range codexsettings.PresetModels {
		selected := value == model
		preset = preset || selected
		options = append(options, discord.NewStringSelectMenuOption(value, value).WithDefault(selected))
	}
	options = append(options,
		discord.NewStringSelectMenuOption("Codex 默认", "__default__").WithDefault(model == ""),
		discord.NewStringSelectMenuOption("自定义", "__custom__").WithDefault(model != "" && !preset))
	custom := discord.NewShortTextInput("custom_model").WithRequired(false).WithMaxLength(128)
	if model != "" && !preset {
		custom = custom.WithValue(model)
	}
	return options, custom
}

func effortModalSelect(effort string) discord.StringSelectMenuComponent {
	return discord.NewStringSelectMenu("reasoning_effort", "选择思考等级",
		discord.NewStringSelectMenuOption("Codex 默认", "__default__").WithDefault(effort == ""),
		discord.NewStringSelectMenuOption("轻", "low").WithDefault(effort == "low"),
		discord.NewStringSelectMenuOption("中", "medium").WithDefault(effort == "medium"),
		discord.NewStringSelectMenuOption("高", "high").WithDefault(effort == "high"),
		discord.NewStringSelectMenuOption("极高", "xhigh").WithDefault(effort == "xhigh")).WithRequired(true)
}

func (c *DisgoConnector) createCodexPost(event *events.ModalSubmitInteractionCreate) {
	forumDiscordID := strings.TrimPrefix(event.Data.CustomID, newCodexModalPrefix)
	body := strings.TrimSpace(event.Data.Text("task"))
	model := firstModalValue(event.Data.StringValues("model"))
	customSelected := model == "__custom__"
	switch model {
	case "__custom__":
		model = strings.TrimSpace(event.Data.Text("custom_model"))
	case "__default__":
		model = ""
	}
	effort := firstModalValue(event.Data.StringValues("reasoning_effort"))
	if effort == "__default__" {
		effort = ""
	}
	tier := firstModalValue(event.Data.StringValues("service_tier"))
	if err := event.DeferCreateMessage(true); err != nil {
		return
	}
	ctx := context.Background()
	var err error
	if customSelected && model == "" {
		err = errors.New("选择自定义模型时必须填写模型名称")
	}
	if body == "" {
		err = errors.New("任务内容不能为空")
	}
	if err == nil {
		_, _, _, err = c.authorizedForum(ctx, forumDiscordID, event.User().ID.String())
	}
	forumSnowflake, parseErr := snowflake.Parse(forumDiscordID)
	if err == nil {
		err = parseErr
	}
	var threadID string
	if err == nil {
		post, createErr := event.Client().Rest.CreatePostInThreadChannel(forumSnowflake,
			discord.ThreadChannelPostCreate{Name: "Codex 正在生成标题", AutoArchiveDuration: discord.AutoArchiveDuration1w,
				Message: discord.MessageCreate{Content: body}}, disgorest.WithCtx(ctx))
		if createErr != nil {
			err = createErr
		} else {
			threadID = post.ID().String()
			input := IncomingMessage{GuildID: c.guildID, ForumID: forumDiscordID, ThreadID: threadID,
				MessageID: post.Message.ID.String(), DiscordUserID: event.User().ID.String(),
				DisplayName: event.User().EffectiveName(), Username: event.User().Username,
				Title: "Codex 正在生成标题", Body: body, Model: model, ReasoningEffort: effort,
				ServiceTier: tier, ConfigurationConfirmed: true}
			var conversationID uuid.UUID
			conversationID, err = c.conversations.BeginPost(ctx, input)
			if err == nil {
				err = ProjectConversationStatus(ctx, c.manager.db, c.guildID, threadID, conversationID,
					input.MessageID, uuid.Nil, ConversationRunning, "帖子已创建，消息正在进入长期开发环境队列。")
			}
		}
	}
	message := "已创建 Codex 帖子：<#" + threadID + ">"
	if err != nil {
		message = fmt.Sprintf("创建 Codex 帖子失败：%v", err)
	}
	_, _ = event.Client().Rest.UpdateInteractionResponse(event.ApplicationID(), event.Token(),
		discord.MessageUpdate{Content: &message})
}

func firstModalValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
