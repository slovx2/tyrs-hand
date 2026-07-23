package discordintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	disgorest "github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type DisgoConnector struct {
	manager       *Manager
	conversations *ConversationService
	bindings      *BindingService
	guildID       string
	token         string
	logger        *zap.Logger
	client        *bot.Client
}

func NewDisgoConnector(manager *Manager, conversations *ConversationService, bindings *BindingService, guildID, token string, logger *zap.Logger) *DisgoConnector {
	return &DisgoConnector{manager: manager, conversations: conversations, bindings: bindings,
		guildID: guildID, token: token, logger: logger}
}

func (c *DisgoConnector) Open(ctx context.Context, resume *GatewaySession) error {
	gatewayOptions := []gateway.ConfigOpt{gateway.WithIntents(
		gateway.IntentGuilds, gateway.IntentGuildMembers, gateway.IntentGuildMessages, gateway.IntentMessageContent,
	)}
	if resume != nil && resume.SessionID != "" && resume.ResumeURL != "" {
		gatewayOptions = append(gatewayOptions, gateway.WithSessionID(resume.SessionID),
			gateway.WithResumeURL(resume.ResumeURL), gateway.WithSequence(resume.Sequence))
	}
	client, err := disgo.New(c.token,
		bot.WithGatewayConfigOpts(gatewayOptions...),
		bot.WithRestClientConfigOpts(disgorest.WithRateLimiterConfigOpts(disgorest.WithMaxRetries(3))),
		bot.WithRestConfigOpts(disgorest.WithDefaultAllowedMentions(discord.AllowedMentions{})),
		bot.WithEventListenerFunc(c.onReady), bot.WithEventListenerFunc(c.onResumed),
		bot.WithEventListenerFunc(c.onMessage), bot.WithEventListenerFunc(c.onCommand),
		bot.WithEventListenerFunc(c.onComponent), bot.WithEventListenerFunc(c.onModalSubmit),
		bot.WithEventListenerFunc(c.onThreadUpdate),
	)
	if err != nil {
		return err
	}
	c.client = client
	defer client.Close(context.Background())
	if err := c.manager.SetGatewayStatus(ctx, c.guildID, "connecting", nil); err != nil {
		return err
	}
	if err := client.OpenGateway(ctx); err != nil {
		_ = c.manager.SetGatewayStatus(context.Background(), c.guildID, "failed", err)
		return err
	}
	_ = c.manager.SetGatewayStatus(ctx, c.guildID, "connected", nil)
	<-ctx.Done()
	return ctx.Err()
}

func (c *DisgoConnector) onReady(event *events.Ready) {
	if event.User.ID != event.Client().ID() {
		return
	}
	c.persistSession(event.SequenceNumber())
	if err := c.registerCommands(context.Background(), event.Client()); err != nil {
		c.logger.Error("注册 Discord 命令失败", zap.Error(err))
	}
}

func (c *DisgoConnector) onResumed(event *events.Resumed) {
	c.persistSession(event.SequenceNumber())
	_ = c.manager.SetGatewayStatus(context.Background(), c.guildID, "resumed", nil)
}

func (c *DisgoConnector) persistSession(sequence int) {
	if c.client == nil || c.client.Gateway == nil || c.client.Gateway.SessionID() == nil || c.client.Gateway.ResumeURL() == nil {
		return
	}
	_ = c.manager.SaveGatewaySession(context.Background(), GatewaySession{
		GuildID: c.guildID, SessionID: *c.client.Gateway.SessionID(),
		ResumeURL: *c.client.Gateway.ResumeURL(), Sequence: sequence,
	})
}

func (c *DisgoConnector) onMessage(event *events.MessageCreate) {
	if event.GuildID == nil || event.GuildID.String() != c.guildID || event.Message.Author.Bot {
		return
	}
	c.persistSession(event.SequenceNumber())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	eventID := "message:" + event.Message.ID.String()
	inserted, err := c.manager.RecordInboundEvent(ctx, eventID, c.guildID, "MESSAGE_CREATE", event.Message)
	if err != nil || !inserted {
		return
	}
	err = c.handleMessage(ctx, event)
	_ = c.manager.CompleteInboundEvent(context.Background(), eventID, err)
	if err != nil {
		c.logger.Warn("处理 Discord 消息失败", zap.Error(err), zap.String("message_id", event.Message.ID.String()))
	}
}

func (c *DisgoConnector) handleMessage(ctx context.Context, event *events.MessageCreate) error {
	displayName := event.Message.Author.EffectiveName()
	if event.Message.Member != nil {
		displayName = event.Message.Member.EffectiveName()
	}
	_, _ = c.manager.db.ExecContext(ctx, `INSERT INTO discord_members
		(guild_id, discord_user_id, username, display_name, is_bot) VALUES ($1, $2, $3, $4, false)
		ON CONFLICT(guild_id, discord_user_id) DO UPDATE SET username = EXCLUDED.username,
			display_name = EXCLUDED.display_name, active = true, last_synced_at = now()`,
		c.guildID, event.Message.Author.ID.String(), event.Message.Author.Username, displayName)
	input := IncomingMessage{
		GuildID: c.guildID, ThreadID: event.ChannelID.String(), MessageID: event.Message.ID.String(),
		DiscordUserID: event.Message.Author.ID.String(), DisplayName: displayName,
		Username: event.Message.Author.Username, Body: event.Message.Content,
	}
	for _, attachment := range event.Message.Attachments {
		mediaType := ""
		if attachment.ContentType != nil {
			mediaType = *attachment.ContentType
		}
		input.Attachments = append(input.Attachments, IncomingAttachment{ID: attachment.ID.String(), URL: attachment.URL,
			Filename: attachment.Filename, MediaType: mediaType, Size: int64(attachment.Size)})
	}
	if err := c.conversations.PersistAttachments(ctx, &input); err != nil {
		return err
	}
	var exists bool
	if err := c.manager.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM discord_conversations
		WHERE guild_id = $1 AND thread_id = $2)`, c.guildID, input.ThreadID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		err := c.conversations.Reply(ctx, input)
		if errors.Is(err, codexcontrol.ErrControlTerminated) {
			return NewSQLoutbox(c.manager.db).Enqueue(ctx,
				"conversation:terminated-rejection:"+input.MessageID,
				"message.create", "channels/"+input.ThreadID+"/messages", map[string]any{
					"channelId": input.ThreadID, "card": terminatedControlCard(),
				}, "conversation-terminated-"+input.MessageID)
		}
		if errors.Is(err, codexcontrol.ErrControlArchived) {
			return NewSQLoutbox(c.manager.db).Enqueue(ctx,
				"conversation:archived-rejection:"+input.MessageID,
				"message.create", "channels/"+input.ThreadID+"/messages", map[string]any{
					"channelId": input.ThreadID, "card": archivedConversationCard(),
				}, "conversation-archived-"+input.MessageID)
		}
		return err
	}
	channel, err := event.Client().Rest.GetChannel(event.ChannelID, disgorest.WithCtx(ctx))
	if err != nil {
		return err
	}
	guildChannel, ok := channel.(discord.GuildChannel)
	if !ok || guildChannel.ParentID() == nil {
		return nil
	}
	input.ForumID = guildChannel.ParentID().String()
	var developmentForum bool
	if err := c.manager.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM discord_forums f JOIN discord_resources r ON r.id = f.resource_id
		WHERE f.guild_id = $1 AND f.forum_type = 'development' AND r.discord_id = $2
	)`, c.guildID, input.ForumID).Scan(&developmentForum); err != nil {
		return err
	}
	if !developmentForum {
		return nil
	}
	input.Title = channel.Name()
	conversationID, err := c.conversations.BeginPost(ctx, input)
	if err != nil {
		return err
	}
	return ProjectConversationConfiguration(ctx, c.manager.db, input.GuildID, input.ThreadID,
		conversationID, input.MessageID)
}

func (c *DisgoConnector) onCommand(event *events.ApplicationCommandInteractionCreate) {
	if event.GuildID() == nil || event.GuildID().String() != c.guildID {
		return
	}
	data := event.SlashCommandInteractionData()
	path := data.CommandPath()
	if path == "/codex/new" {
		forum, ok := data.OptChannel("forum")
		if !ok {
			_ = event.CreateMessage(discord.NewMessageCreate().WithContent("请选择开发 Forum。").WithEphemeral(true))
			return
		}
		modal, err := c.newCodexModal(context.Background(), forum.ID.String(), event.User().ID.String())
		if err != nil {
			_ = event.CreateMessage(discord.NewMessageCreate().WithContent(err.Error()).WithEphemeral(true))
			return
		}
		_ = event.Modal(modal)
		return
	}
	if err := event.DeferCreateMessage(true); err != nil {
		return
	}
	eventID := "interaction:" + event.ID().String()
	inserted, recordErr := c.manager.RecordInboundEvent(context.Background(), eventID, c.guildID,
		"APPLICATION_COMMAND", map[string]string{"id": event.ID().String(), "userId": event.User().ID.String()})
	if recordErr != nil || !inserted {
		return
	}
	userID := event.User().ID.String()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	content := "操作完成"
	var components []discord.LayoutComponent
	var err error
	defer func() { _ = c.manager.CompleteInboundEvent(context.Background(), eventID, err) }()
	switch path {
	case "/github/bind":
		var link string
		link, err = c.bindings.Start(ctx, c.guildID, userID)
		if err == nil {
			content = "使用此一次性链接绑定 GitHub 身份：" + link
		}
	case "/github/unbind":
		content = "确认解除当前 Discord 用户与 GitHub 的绑定？"
		components = []discord.LayoutComponent{discord.NewActionRow(discord.NewDangerButton("确认解绑", "github-unbind-confirm:"+userID))}
	case "/codex/stop":
		var count int64
		count, err = c.conversations.Stop(ctx, c.guildID, event.Channel().ID().String(), userID)
		if err == nil {
			content = fmt.Sprintf("已停止 %d 个正在运行或排队的任务。", count)
		}
	case "/codex/restore":
		threadID := event.Channel().ID().String()
		if rawPost, ok := data.OptString("post"); ok && strings.TrimSpace(rawPost) != "" {
			threadID, err = parseDiscordPostReference(rawPost)
		}
		if err == nil {
			var state workerprotocol.ThreadLifecycleState
			state, err = c.conversations.Restore(ctx, c.guildID, threadID, userID, nil)
			if err == nil {
				if state.Status == "completed" {
					content = "会话已经处于可用状态。"
				} else {
					content = "恢复请求已提交；Codex 确认恢复后会自动解锁原 Post。"
				}
			}
		}
	case "/codex/archive":
		threadID := event.Channel().ID().String()
		if rawPost, ok := data.OptString("post"); ok && strings.TrimSpace(rawPost) != "" {
			threadID, err = parseDiscordPostReference(rawPost)
		}
		if err == nil {
			var state workerprotocol.ThreadLifecycleState
			state, err = c.conversations.Archive(ctx, c.guildID, threadID, userID)
			if err == nil {
				switch state.Status {
				case "completed":
					content = "会话已经归档。"
				case "waiting_for_turn":
					content = "归档请求已提交；当前 Turn 完成后会自动归档并隐藏原 Post。"
				default:
					content = "归档请求已提交；Codex 确认归档后会自动锁定并隐藏原 Post。"
				}
			}
		}
	default:
		err = errors.New("未知 Discord 命令")
	}
	if err != nil {
		content = err.Error()
	}
	update := discord.MessageUpdate{Content: &content}
	if components != nil {
		update.Components = &components
	}
	_, _ = event.Client().Rest.UpdateInteractionResponse(event.ApplicationID(), event.Token(), update)
}

func (c *DisgoConnector) onComponent(event *events.ComponentInteractionCreate) {
	if event.GuildID() == nil || event.GuildID().String() != c.guildID {
		return
	}
	customID := event.Data.CustomID()
	if strings.HasPrefix(customID, "codex-progress-") {
		c.updateConversationProgressPage(event, customID)
		return
	}
	if strings.HasPrefix(customID, interactiveButtonPrefix) {
		c.answerInteractiveComponent(event, customID)
		return
	}
	if strings.HasPrefix(customID, "codex-restore:") {
		c.restoreConversationComponent(event, customID)
		return
	}
	eventID := "interaction:" + event.ID().String()
	inserted, err := c.manager.RecordInboundEvent(context.Background(), eventID, c.guildID,
		"MESSAGE_COMPONENT", map[string]string{"id": event.ID().String(), "customId": customID})
	if err != nil || !inserted {
		return
	}
	defer func() { _ = c.manager.CompleteInboundEvent(context.Background(), eventID, nil) }()
	if strings.HasPrefix(customID, "codex-config-start:") {
		c.startConfiguredConversation(event, strings.TrimPrefix(customID, "codex-config-start:"))
		return
	}
	if customID == "codex-new-open" {
		c.showForumSelector(event)
		return
	}
	if customID == "codex-new-forum" {
		values := event.ChannelSelectMenuInteractionData().Values
		if len(values) == 0 {
			_ = event.CreateMessage(discord.NewMessageCreate().WithContent("请选择开发 Forum。").WithEphemeral(true))
			return
		}
		modal, err := c.newCodexModal(context.Background(), values[0].String(), event.User().ID.String())
		if err != nil {
			_ = event.CreateMessage(discord.NewMessageCreate().WithContent(err.Error()).WithEphemeral(true))
			return
		}
		_ = event.Modal(modal)
		return
	}
	if strings.HasPrefix(customID, "codex-config-edit:") {
		c.editConversationConfiguration(event, strings.TrimPrefix(customID, "codex-config-edit:"))
		return
	}
	const prefix = "github-unbind-confirm:"
	if !strings.HasPrefix(customID, prefix) || event.GuildID() == nil {
		return
	}
	expectedUser := strings.TrimPrefix(customID, prefix)
	if expectedUser != event.User().ID.String() {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent("只有发起解绑的用户可以确认。").WithEphemeral(true))
		return
	}
	err = c.bindings.Unbind(context.Background(), event.GuildID().String(), expectedUser, true)
	content := "GitHub 身份已解绑，可以重新绑定。"
	if err != nil {
		content = err.Error()
	}
	empty := []discord.LayoutComponent{}
	_ = event.UpdateMessage(discord.MessageUpdate{Content: &content, Components: &empty})
}

func (c *DisgoConnector) updateConversationProgressPage(event *events.ComponentInteractionCreate,
	customID string,
) {
	_, runID, page, err := parseProgressButton(customID)
	if err != nil || event.GuildID() == nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent("这个翻页按钮无效，请使用卡片上的最新按钮。").WithEphemeral(true))
		return
	}
	card, err := c.conversationProgressPage(context.Background(), event.GuildID().String(),
		event.Message.ChannelID.String(), event.Message.ID.String(), runID, page)
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent("这张卡片已过期，无法继续翻页。").WithEphemeral(true))
		return
	}
	components, err := discordCardComponents(card)
	if err != nil {
		_ = event.CreateMessage(discord.NewMessageCreate().WithContent("卡片暂时无法更新，请稍后重试。").WithEphemeral(true))
		return
	}
	update := discord.NewMessageUpdateV2(components...)
	emptyContent := ""
	emptyEmbeds := []discord.Embed{}
	update.Content, update.Embeds = &emptyContent, &emptyEmbeds
	update.AllowedMentions = &discord.AllowedMentions{}
	_ = event.UpdateMessage(update)
}

func parseProgressButton(customID string) (string, uuid.UUID, int, error) {
	if !strings.HasPrefix(customID, "codex-progress-") {
		return "", uuid.Nil, 0, errors.New("discord 翻页按钮前缀无效")
	}
	parts := strings.Split(strings.TrimPrefix(customID, "codex-progress-"), ":")
	if len(parts) != 3 || (parts[0] != "older" && parts[0] != "newer" && parts[0] != "latest") {
		return "", uuid.Nil, 0, errors.New("discord 翻页动作无效")
	}
	runID, err := uuid.Parse(parts[1])
	if err != nil {
		return "", uuid.Nil, 0, err
	}
	page, err := strconv.Atoi(parts[2])
	if err != nil || page < 0 {
		return "", uuid.Nil, 0, errors.New("discord 翻页页码无效")
	}
	return parts[0], runID, page, nil
}

func (c *DisgoConnector) conversationProgressPage(ctx context.Context, guildID, channelID,
	messageID string, runID uuid.UUID, page int,
) (ComponentCardPayload, error) {
	if page < 0 {
		return ComponentCardPayload{}, errors.New("discord 翻页页码无效")
	}
	var rawPayload json.RawMessage
	err := c.manager.db.QueryRowContext(ctx, `SELECT desired_payload
		FROM discord_projections WHERE guild_id = $1 AND resource_id = $2 AND message_id = $3
		AND projection_key LIKE 'conversation:%'`, guildID, channelID, messageID).Scan(&rawPayload)
	var desired struct {
		Progress conversationProgressPayload `json:"progress"`
	}
	if err == nil {
		err = json.Unmarshal(rawPayload, &desired)
	}
	if err != nil || desired.Progress.RunID != runID.String() {
		return ComponentCardPayload{}, errors.New("discord 翻页卡片与 Run 不匹配")
	}
	timeline, err := conversationTimelineForRun(ctx, c.manager.db, runID,
		desired.Progress.Summary)
	if err != nil || page >= len(timeline.Pages) {
		return ComponentCardPayload{}, errors.New("discord 翻页目标不存在")
	}
	return conversationProgressCard(desired.Progress.State, timeline, page, runID.String()), nil
}

func (c *DisgoConnector) registerCommands(ctx context.Context, client *bot.Client) error {
	guildID, err := snowflake.Parse(c.guildID)
	if err != nil {
		return err
	}
	applicationID := client.ApplicationID
	if applicationID == 0 {
		applicationID = client.ID()
	}
	commands := []discord.ApplicationCommandCreate{
		discord.SlashCommandCreate{Name: "github", Description: "管理 GitHub 身份绑定", Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionSubCommand{Name: "bind", Description: "绑定 GitHub 身份"},
			discord.ApplicationCommandOptionSubCommand{Name: "unbind", Description: "解绑 GitHub 身份"},
		}},
		discord.SlashCommandCreate{Name: "codex", Description: "管理当前 Codex 会话", Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionSubCommand{Name: "new", Description: "新建 Codex Forum 帖子", Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionChannel{Name: "forum", Description: "目标开发 Forum", Required: true,
					ChannelTypes: []discord.ChannelType{discord.ChannelTypeGuildForum}},
			}},
			discord.ApplicationCommandOptionSubCommand{Name: "stop", Description: "停止当前会话的活动任务"},
			discord.ApplicationCommandOptionSubCommand{Name: "archive", Description: "归档 Codex 会话并隐藏原 Post", Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{Name: "post", Description: "原 Post mention 或 ID；在原 Post 内可省略"},
			}},
			discord.ApplicationCommandOptionSubCommand{Name: "restore", Description: "恢复已归档的 Codex 会话", Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{Name: "post", Description: "原 Post mention 或 ID；在原 Post 内可省略"},
			}},
		}},
	}
	_, err = client.Rest.SetGuildCommands(applicationID, guildID, commands, disgorest.WithCtx(ctx))
	return err
}

func parseDiscordPostReference(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "<#")
	value = strings.TrimSuffix(value, ">")
	id, err := snowflake.Parse(value)
	if err != nil {
		return "", errors.New("post 参数必须是 Discord Post mention 或 ID")
	}
	return id.String(), nil
}

func (c *DisgoConnector) restoreConversationComponent(event *events.ComponentInteractionCreate,
	customID string,
) {
	parts := strings.Split(strings.TrimPrefix(customID, "codex-restore:"), ":")
	if len(parts) != 2 {
		_ = event.CreateMessage(discord.NewMessageCreate().
			WithContent("这个恢复按钮无效，请使用 `/codex restore`。").WithEphemeral(true))
		return
	}
	conversationID, err := uuid.Parse(parts[0])
	revision, revisionErr := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || revisionErr != nil || revision < 0 {
		_ = event.CreateMessage(discord.NewMessageCreate().
			WithContent("这个恢复按钮无效，请使用 `/codex restore`。").WithEphemeral(true))
		return
	}
	if err := event.DeferCreateMessage(true); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var threadID string
	err = c.manager.db.QueryRowContext(ctx, `SELECT thread_id FROM discord_conversations
		WHERE id = $1 AND guild_id = $2`, conversationID, c.guildID).Scan(&threadID)
	content := "恢复请求已提交；Codex 确认恢复后会自动解锁原 Post。"
	if err == nil {
		var state workerprotocol.ThreadLifecycleState
		state, err = c.conversations.Restore(ctx, c.guildID, threadID,
			event.User().ID.String(), &revision)
		if err == nil && state.Status == "completed" {
			content = "会话已经处于可用状态。"
		}
	}
	if err != nil {
		content = err.Error()
	}
	_, _ = event.Client().Rest.UpdateInteractionResponse(event.ApplicationID(), event.Token(),
		discord.MessageUpdate{Content: &content})
}

func (c *DisgoConnector) onThreadUpdate(event *events.ThreadUpdate) {
	if event.GuildID.String() != c.guildID {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var conversationID uuid.UUID
	var state string
	err := c.manager.db.QueryRowContext(ctx, `SELECT id, lifecycle_state
		FROM discord_conversations WHERE guild_id = $1 AND thread_id = $2`,
		c.guildID, event.ThreadID.String()).Scan(&conversationID, &state)
	if err != nil || (state != "active" && state != "archived") {
		return
	}
	archived := event.Thread.ThreadMetadata.Archived
	locked := event.Thread.ThreadMetadata.Locked
	if state == "active" {
		// Discord 的未锁定归档只表示客户端隐藏，不改变 Codex 生命周期。
		if (archived && !locked) || (!archived && !locked) {
			return
		}
	} else if archived && locked {
		return
	}
	tx, err := c.manager.db.BeginTx(ctx, nil)
	if err == nil {
		err = EnqueueConversationLifecycleTx(ctx, tx, conversationID)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		c.logger.Warn("重新投影 Discord 会话生命周期失败",
			zap.String("thread_id", event.ThreadID.String()), zap.Error(err))
	}
}

var _ GatewayConnector = (*DisgoConnector)(nil)
