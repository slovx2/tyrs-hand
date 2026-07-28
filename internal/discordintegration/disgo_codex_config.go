package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	disgorest "github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
)

const runtimeConfigurationModalPrefix = "codex-runtime-config-modal:"
const newCodexModalPrefix = "codex-new-modal:"

func (c *DisgoConnector) runtimeConfigurationModal(ctx context.Context,
	conversationID uuid.UUID, revision int64, userID string,
) (discord.ModalCreate, error) {
	threadID, err := conversationThreadID(ctx, c.manager.db, conversationID)
	if err != nil {
		return discord.ModalCreate{}, err
	}
	state, err := c.conversations.ConversationMode(ctx, c.guildID, threadID, userID)
	if err != nil {
		return discord.ModalCreate{}, err
	}
	if state.ConversationID != conversationID || state.SettingsRevision != revision {
		return discord.ModalCreate{}, errors.New("设置卡已过期，请重新运行 `/codex config`")
	}
	modelOptions, custom := modelModalOptions(state.Model)
	modelSelect := discord.NewStringSelectMenu("model", "选择模型", modelOptions...).WithRequired(true)
	tierSelect := discord.NewStringSelectMenu("service_tier", "选择速度",
		discord.NewStringSelectMenuOption("标准", "standard").WithDefault(state.ServiceTier != "fast"),
		discord.NewStringSelectMenuOption("快速", "fast").WithDefault(state.ServiceTier == "fast")).WithRequired(true)
	effortSelect := effortModalSelect(state.ReasoningEffort)
	return discord.NewModalCreate(fmt.Sprintf("%s%s:%d", runtimeConfigurationModalPrefix,
		conversationID, revision), "调整 Codex 运行参数",
		discord.NewLabel("模型", modelSelect), discord.NewLabel("自定义模型", custom),
		discord.NewLabel("思考等级", effortSelect), discord.NewLabel("速度", tierSelect)), nil
}

func conversationThreadID(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, conversationID uuid.UUID) (string, error) {
	var threadID string
	err := db.QueryRowContext(ctx, `SELECT thread_id FROM discord_conversations WHERE id = $1`,
		conversationID).Scan(&threadID)
	return threadID, err
}

func (c *DisgoConnector) startConfiguredConversation(event *events.ComponentInteractionCreate, rawID string) {
	parts := strings.Split(rawID, ":")
	if len(parts) != 2 {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(
			"这个启动按钮无效，请使用最新设置卡。").WithEphemeral(true))
		return
	}
	id, err := uuid.Parse(parts[0])
	revision, revisionErr := strconv.ParseInt(parts[1], 10, 64)
	if err == nil && (revisionErr != nil || revision < 0) {
		err = errors.New("启动按钮 revision 无效")
	}
	mode := "default"
	if err == nil {
		var stale bool
		stale, err = c.conversations.FinalizeConfigurationRevision(context.Background(), id,
			event.User().ID.String(), revision)
		if stale && err == nil {
			state, stateErr := c.conversations.ConversationMode(context.Background(), c.guildID,
				event.Channel().ID().String(), event.User().ID.String())
			if stateErr != nil || !state.Awaiting {
				_ = event.CreateMessage(discord.NewMessageCreate().WithContent(
					"这张等待卡已经过期；当前会话设置没有改变。").WithEphemeral(true))
				return
			}
			components, componentErr := discordCardComponents(conversationModeCard(state,
				"这张等待卡已经过期，已刷新为最新设置；请再次确认启动。"))
			if componentErr != nil {
				_ = event.CreateMessage(discord.NewMessageCreate().WithContent(
					"这张等待卡已经过期，请使用最新设置卡再次确认启动。").WithEphemeral(true))
				return
			}
			update := discord.NewMessageUpdateV2(components...)
			emptyContent := ""
			emptyEmbeds := []discord.Embed{}
			update.Content, update.Embeds = &emptyContent, &emptyEmbeds
			update.AllowedMentions = &discord.AllowedMentions{}
			_ = event.UpdateMessage(update)
			return
		}
	}
	if err == nil {
		err = c.manager.db.QueryRowContext(context.Background(), `SELECT collaboration_mode
			FROM discord_conversations WHERE id = $1`, id).Scan(&mode)
	}
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(err.Error()).WithEphemeral(true))
		return
	}
	timeline := ConversationTimeline{Pages: []string{"已使用当前设置启动"},
		Duration: time.Second}
	components, componentErr := discordCardComponents(conversationProgressCard(ConversationRunning,
		timeline, 0, "", mode))
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
	if strings.HasPrefix(event.Data.CustomID, runtimeConfigurationModalPrefix) {
		c.saveRuntimeConfiguration(event)
		return
	}
}

func (c *DisgoConnector) saveRuntimeConfiguration(event *events.ModalSubmitInteractionCreate) {
	parts := strings.Split(strings.TrimPrefix(event.Data.CustomID,
		runtimeConfigurationModalPrefix), ":")
	if len(parts) != 2 {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(
			"这个设置表单无效，请重新运行 `/codex config`。").WithEphemeral(true))
		return
	}
	conversationID, parseErr := uuid.Parse(parts[0])
	revision, revisionErr := strconv.ParseInt(parts[1], 10, 64)
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
	var err error
	if parseErr != nil {
		err = parseErr
	} else if revisionErr != nil || revision < 0 {
		err = errors.New("设置表单 revision 无效")
	} else if customSelected && model == "" {
		err = errors.New("选择自定义模型时必须填写模型名称")
	}
	var result ConfigurationUpdate
	if err == nil {
		result, err = c.conversations.SetRuntimePreferences(context.Background(),
			c.guildID, event.Channel().ID().String(), event.User().ID.String(), conversationID,
			revision, ConversationConfiguration{Model: model, ReasoningEffort: effort,
				ServiceTier: tier})
	}
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(err.Error()).WithEphemeral(true))
		return
	}
	state := result.State
	notice := "运行参数已保存；下一次 Turn 开始时生效。"
	if result.Stale {
		notice = "这个设置表单已经过期，已显示当前最新设置。"
	} else if len(result.Changes) == 0 {
		notice = "设置没有变化。"
	} else if announceErr := c.announceConversationConfig(context.Background(),
		event.Channel().ID().String(), event.ID().String(), event.User().ID.String(),
		result.Changes); announceErr != nil {
		notice += " 公开结果暂时发送失败。"
	}
	components, componentErr := discordCardComponents(conversationModeCard(state, notice))
	if componentErr != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent(notice).WithEphemeral(true))
		return
	}
	message := discord.NewMessageCreateV2(components...).WithEphemeral(true)
	_ = event.CreateMessage(message)
}

func (c *DisgoConnector) showForumSelector(event *events.ComponentInteractionCreate) {
	selector := discord.NewChannelSelectMenu("codex-new-forum", "选择开发 Forum").
		WithChannelTypes(discord.ChannelTypeGuildForum).WithRequired(true)
	message := discord.NewMessageCreate().WithContent("选择要创建 Codex 帖子的开发 Forum：").
		WithComponents(discord.NewActionRow(selector)).WithEphemeral(true)
	_ = event.CreateMessage(message)
}

func (c *DisgoConnector) newCodexModal(ctx context.Context, forumDiscordID, userID,
	mode string,
) (discord.ModalCreate, error) {
	forumID, repositoryID, profileID, err := c.authorizedForum(ctx, forumDiscordID, userID)
	if err != nil {
		return discord.ModalCreate{}, err
	}
	preferences, err := codexsettings.NewService(c.manager.db).Resolve(ctx, repositoryID, forumID, profileID)
	if err != nil {
		return discord.ModalCreate{}, err
	}
	userPreferences, remembered, err := loadUserCodexPreferences(ctx, c.manager.db,
		c.guildID, userID)
	if err != nil {
		return discord.ModalCreate{}, err
	}
	if remembered {
		applyUserCodexPreferences(&preferences, userPreferences)
	}
	modelOptions, custom := modelModalOptions(preferences.Model)
	modelSelect := discord.NewStringSelectMenu("model", "选择模型", modelOptions...).WithRequired(true)
	tierSelect := discord.NewStringSelectMenu("service_tier", "选择服务等级",
		discord.NewStringSelectMenuOption("标准", "standard").WithDefault(preferences.ServiceTier != "fast"),
		discord.NewStringSelectMenuOption("快速", "fast").WithDefault(preferences.ServiceTier == "fast")).WithRequired(true)
	effortSelect := effortModalSelect(preferences.ReasoningEffort)
	task := discord.NewParagraphTextInput("task").WithRequired(true).WithMinLength(1).WithMaxLength(2000).
		WithPlaceholder("描述希望 Codex 完成的任务")
	if mode == "" {
		mode = userPreferences.CollaborationMode
		if mode == "" {
			mode = "default"
		}
	}
	if mode != "default" && mode != "plan" {
		return discord.ModalCreate{}, errors.New("新建会话模式无效")
	}
	return discord.NewModalCreate(newCodexModalPrefix+forumDiscordID+":"+mode, "新建 Codex 帖子",
		discord.NewLabel("任务", task), discord.NewLabel("模型", modelSelect),
		discord.NewLabel("自定义模型", custom), discord.NewLabel("服务等级", tierSelect),
		discord.NewLabel("思考等级", effortSelect)), nil
}

func (c *DisgoConnector) authorizedForum(ctx context.Context, forumDiscordID, userID string) (
	uuid.UUID, uuid.UUID, uuid.UUID, error,
) {
	var forumID, profileID uuid.UUID
	var owner string
	err := c.manager.db.QueryRowContext(ctx, `SELECT f.id, f.owner_discord_user_id,
		(SELECT id FROM agent_profiles ORDER BY created_at LIMIT 1)
		FROM discord_forums f JOIN discord_resources r ON r.id = f.resource_id
		JOIN development_projects project ON project.id=f.development_project_id
		JOIN discord_development_environments environment
			ON environment.id=f.development_environment_id
		WHERE f.guild_id=$1 AND r.discord_id=$2 AND f.forum_type='development'
			AND f.binding_status='active'
			AND project.availability_status='available'
			AND environment.status='running'`,
		c.guildID, forumDiscordID).Scan(&forumID, &owner, &profileID)
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
	return forumID, uuid.Nil, profileID, nil
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
	parts := strings.Split(strings.TrimPrefix(event.Data.CustomID, newCodexModalPrefix), ":")
	if len(parts) != 2 {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent("新建会话参数无效。").WithEphemeral(true))
		return
	}
	forumDiscordID, mode := parts[0], parts[1]
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
				ServiceTier: tier, CollaborationMode: mode, ConfigurationConfirmed: true,
				RememberPreferences: true}
			_, err = c.conversations.BeginPost(ctx, input)
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
