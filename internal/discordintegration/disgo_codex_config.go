package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	disgorest "github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcatalog"
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
	var workspaceID uuid.UUID
	if err := c.manager.db.QueryRowContext(ctx, `SELECT forum.workspace_id
		FROM discord_conversations conversation JOIN discord_forums forum
		ON forum.id=conversation.forum_id WHERE conversation.id=$1`, conversationID).
		Scan(&workspaceID); err != nil {
		return discord.ModalCreate{}, err
	}
	models, err := codexModelsForEnvironment(ctx, c.manager.db, workspaceID)
	if err != nil {
		return discord.ModalCreate{}, err
	}
	modelOptions, custom := modelModalOptions(state.Model, models)
	modelSelect := discord.NewStringSelectMenu("model", "选择模型", modelOptions...).WithRequired(true)
	tierSelect := tierModalSelect(state.ServiceTier, state.Model, models, "选择速度")
	effortSelect := effortModalSelect(state.ReasoningEffort,
		reasoningEffortsForModel(state.Model, models))
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
		var workspaceID uuid.UUID
		err = c.manager.db.QueryRowContext(context.Background(), `SELECT forum.workspace_id
			FROM discord_conversations conversation JOIN discord_forums forum
			ON forum.id=conversation.forum_id WHERE conversation.id=$1`, conversationID).
			Scan(&workspaceID)
		if err == nil {
			var models []codexcatalog.Model
			models, err = codexModelsForEnvironment(context.Background(), c.manager.db, workspaceID)
			if err == nil {
				err = validateKnownModelSelection(model, effort, tier, models)
			}
		}
	}
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
	_, _, _, workspaceID, err := c.authorizedForum(ctx,
		forumDiscordID, userID)
	if err != nil {
		return discord.ModalCreate{}, err
	}
	preferences := codexsettings.EffectivePreferences{}
	userPreferences, remembered, err := loadUserCodexPreferences(ctx, c.manager.db,
		c.guildID, userID)
	if err != nil {
		return discord.ModalCreate{}, err
	}
	if remembered {
		applyUserCodexPreferences(&preferences, userPreferences)
	}
	models, err := codexModelsForEnvironment(ctx, c.manager.db, workspaceID)
	if err != nil {
		return discord.ModalCreate{}, err
	}
	modelOptions, custom := modelModalOptions(preferences.Model, models)
	modelSelect := discord.NewStringSelectMenu("model", "选择模型", modelOptions...).WithRequired(true)
	tierSelect := tierModalSelect(preferences.ServiceTier, preferences.Model, models, "选择服务等级")
	effortSelect := effortModalSelect(preferences.ReasoningEffort,
		reasoningEffortsForModel(preferences.Model, models))
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
	uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, error,
) {
	var forumID, profileID, workspaceID uuid.UUID
	var owner string
	err := c.manager.db.QueryRowContext(ctx, `SELECT f.id,
		(SELECT id FROM agent_profiles ORDER BY created_at LIMIT 1),
		f.workspace_id, f.owner_discord_user_id
		FROM discord_forums f JOIN discord_resources r ON r.id = f.resource_id
		JOIN workspace_projects project ON project.id=f.workspace_project_id
		JOIN worker_workspaces workspace
			ON workspace.id=f.workspace_id
		WHERE f.guild_id=$1 AND r.discord_id=$2 AND f.forum_type='workspace'
			AND f.binding_status='active'
			AND project.availability_status='available'
			`,
		c.guildID, forumDiscordID).Scan(&forumID, &profileID, &workspaceID, &owner)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, errors.New("所选频道不是可用的开发 Forum")
	}
	if userID != owner {
		var operator bool
		err = c.manager.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM discord_forum_access
			WHERE forum_id = $1 AND discord_user_id = $2 AND access_level = 'operator')`, forumID, userID).
			Scan(&operator)
		if err != nil || !operator {
			return uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil,
				errors.New("当前用户没有在该 Forum 新建 Codex 会话的权限")
		}
	}
	return forumID, uuid.Nil, profileID, workspaceID, nil
}

func codexModelsForEnvironment(ctx context.Context, db *sql.DB,
	workspaceID uuid.UUID,
) ([]codexcatalog.Model, error) {
	catalogs, err := codexcatalog.WorkspaceCatalogs(ctx, db, []uuid.UUID{workspaceID})
	if err != nil {
		return nil, err
	}
	return codexcatalog.Models(catalogs), nil
}

func modelModalOptions(model string, models []codexcatalog.Model) (
	[]discord.StringSelectMenuOption, discord.TextInputComponent,
) {
	if len(models) > 23 {
		models = models[:23]
	}
	options := make([]discord.StringSelectMenuOption, 0, len(models)+2)
	preset := false
	for _, value := range models {
		selected := value.ID == model
		preset = preset || selected
		options = append(options, discord.NewStringSelectMenuOption(value.ID, value.ID).WithDefault(selected))
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

func selectedCatalogModel(model string, models []codexcatalog.Model) (codexcatalog.Model, bool) {
	for _, option := range models {
		if option.ID == model || (model == "" && option.IsDefault) {
			return option, true
		}
	}
	return codexcatalog.Model{}, false
}

func reasoningEffortsForModel(model string, models []codexcatalog.Model) []string {
	selected, ok := selectedCatalogModel(model, models)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(selected.SupportedReasoningEfforts))
	for _, effort := range selected.SupportedReasoningEfforts {
		if effort.ReasoningEffort != "" {
			result = append(result, effort.ReasoningEffort)
		}
	}
	return result
}

func tierModalSelect(tier, model string, models []codexcatalog.Model,
	placeholder string,
) discord.StringSelectMenuComponent {
	selected, known := selectedCatalogModel(model, models)
	fast := known && codexcatalog.SupportsFast(selected)
	options := []discord.StringSelectMenuOption{
		discord.NewStringSelectMenuOption("标准", "standard").WithDefault(tier != "fast" || !fast),
	}
	if fast {
		options = append(options,
			discord.NewStringSelectMenuOption("快速", "fast").WithDefault(tier == "fast"))
	}
	return discord.NewStringSelectMenu("service_tier", placeholder, options...).WithRequired(true)
}

func validateKnownModelSelection(model, effort, tier string,
	models []codexcatalog.Model,
) error {
	selected, known := selectedCatalogModel(model, models)
	if !known {
		return nil
	}
	if tier == "fast" && !codexcatalog.SupportsFast(selected) {
		return fmt.Errorf("模型 %s 不支持快速模式", selected.ID)
	}
	if effort != "" && !slices.Contains(reasoningEffortsForModel(model, models), effort) {
		return fmt.Errorf("模型 %s 不支持思考等级 %s", selected.ID, effort)
	}
	return nil
}

func effortModalSelect(effort string, efforts []string) discord.StringSelectMenuComponent {
	options := []discord.StringSelectMenuOption{
		discord.NewStringSelectMenuOption("Codex 默认", "__default__").WithDefault(effort == ""),
	}
	if effort != "" && !slices.Contains(efforts, effort) {
		efforts = append([]string{effort}, efforts...)
	}
	if len(efforts) > 24 {
		efforts = efforts[:24]
	}
	for _, value := range efforts {
		options = append(options, discord.NewStringSelectMenuOption(value, value).
			WithDefault(effort == value))
	}
	return discord.NewStringSelectMenu("reasoning_effort", "选择思考等级", options...).
		WithRequired(true)
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
	var workspaceID uuid.UUID
	if customSelected && model == "" {
		err = errors.New("选择自定义模型时必须填写模型名称")
	}
	if body == "" {
		err = errors.New("任务内容不能为空")
	}
	if err == nil {
		_, _, _, workspaceID, err = c.authorizedForum(ctx, forumDiscordID, event.User().ID.String())
	}
	if err == nil {
		var models []codexcatalog.Model
		models, err = codexModelsForEnvironment(ctx, c.manager.db, workspaceID)
		if err == nil {
			err = validateKnownModelSelection(model, effort, tier, models)
		}
	}
	forumSnowflake, parseErr := snowflake.Parse(forumDiscordID)
	if err == nil {
		err = parseErr
	}
	var threadID string
	if err == nil {
		post, createErr := event.Client().Rest.CreatePostInThreadChannel(forumSnowflake,
			discord.ThreadChannelPostCreate{Name: "Codex 正在生成标题", AutoArchiveDuration: discord.AutoArchiveDuration3d,
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
