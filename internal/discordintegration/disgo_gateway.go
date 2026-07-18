package discordintegration

import (
	"context"
	"errors"
	"fmt"
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
		bot.WithEventListenerFunc(c.onComponent),
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
	var exists bool
	if err := c.manager.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM discord_conversations
		WHERE guild_id = $1 AND thread_id = $2)`, c.guildID, input.ThreadID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return c.conversations.Reply(ctx, input)
	}
	channel, err := event.Client().Rest.GetChannel(event.ChannelID, disgorest.WithCtx(ctx))
	if err != nil {
		return err
	}
	guildChannel, ok := channel.(discord.GuildChannel)
	if !ok || guildChannel.ParentID() == nil {
		return errors.New("新 Discord Conversation 不在 Forum Post 中")
	}
	input.ForumID = guildChannel.ParentID().String()
	input.Title = channel.Name()
	conversationID, err := c.conversations.BeginPost(ctx, input)
	if err != nil {
		return err
	}
	return NewSQLoutbox(c.manager.db).Enqueue(ctx, "conversation:workspace:"+conversationID.String(),
		"message.create", "channels/"+input.ThreadID+"/messages", map[string]any{
			"channelId": input.ThreadID,
			"content":   "选择此会话使用的工作区。",
			"buttons": []map[string]string{
				{"label": "空白工作区", "customId": "conversation-blank:" + conversationID.String(), "style": "primary"},
				{"label": "选择仓库", "customId": "conversation-repository:" + conversationID.String(), "style": "secondary"},
			},
		}, "conversation-workspace-"+conversationID.String())
}

func (c *DisgoConnector) onCommand(event *events.ApplicationCommandInteractionCreate) {
	if event.GuildID() == nil || event.GuildID().String() != c.guildID {
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
	data := event.SlashCommandInteractionData()
	path := data.CommandPath()
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
	eventID := "interaction:" + event.ID().String()
	inserted, err := c.manager.RecordInboundEvent(context.Background(), eventID, c.guildID,
		"MESSAGE_COMPONENT", map[string]string{"id": event.ID().String(), "customId": customID})
	if err != nil || !inserted {
		return
	}
	defer func() { _ = c.manager.CompleteInboundEvent(context.Background(), eventID, nil) }()
	if strings.HasPrefix(customID, "conversation-blank:") {
		c.activateBlank(event, strings.TrimPrefix(customID, "conversation-blank:"))
		return
	}
	if strings.HasPrefix(customID, "conversation-repository:") {
		c.showRepositorySelect(event, strings.TrimPrefix(customID, "conversation-repository:"))
		return
	}
	if strings.HasPrefix(customID, "conversation-repository-select:") {
		c.activateRepository(event, strings.TrimPrefix(customID, "conversation-repository-select:"))
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

func (c *DisgoConnector) activateBlank(event *events.ComponentInteractionCreate, rawConversationID string) {
	if err := event.DeferUpdateMessage(); err != nil {
		return
	}
	conversationID, err := uuid.Parse(rawConversationID)
	profileID := uuid.Nil
	if err == nil {
		err = c.manager.db.QueryRowContext(context.Background(), "SELECT id FROM agent_profiles ORDER BY created_at LIMIT 1").Scan(&profileID)
	}
	if err == nil {
		err = c.conversations.Activate(context.Background(), conversationID, profileID, nil, event.User().ID.String())
	}
	content := "已选择空白工作区，首条消息进入队列。"
	if err != nil {
		content = err.Error()
	}
	empty := []discord.LayoutComponent{}
	_, _ = event.Client().Rest.UpdateInteractionResponse(event.ApplicationID(), event.Token(),
		discord.MessageUpdate{Content: &content, Components: &empty})
}

func (c *DisgoConnector) showRepositorySelect(event *events.ComponentInteractionCreate, rawConversationID string) {
	if err := event.DeferCreateMessage(true); err != nil {
		return
	}
	rows, err := c.manager.db.QueryContext(context.Background(), `SELECT r.id::text, r.owner || '/' || r.name
		FROM repositories r JOIN discord_forums f ON f.repository_id = r.id AND f.forum_type = 'repository'
		JOIN discord_forum_access a ON a.forum_id = f.id
		WHERE a.discord_user_id = $1 AND a.access_level = 'readonly' AND r.enabled = true
		ORDER BY lower(r.owner), lower(r.name) LIMIT 25`, event.User().ID.String())
	var options []discord.StringSelectMenuOption
	if err == nil {
		for rows.Next() {
			var id, name string
			if scanErr := rows.Scan(&id, &name); scanErr != nil {
				err = scanErr
				break
			}
			options = append(options, discord.NewStringSelectMenuOption(name, id))
		}
		_ = rows.Close()
	}
	content := "选择一个有权访问的仓库。"
	var components []discord.LayoutComponent
	if err == nil && len(options) > 0 {
		menu := discord.NewStringSelectMenu("conversation-repository-select:"+rawConversationID, "仓库", options...)
		components = []discord.LayoutComponent{discord.NewActionRow(menu)}
	} else if err == nil {
		content = "当前没有已同步且具备读取权限的仓库。"
	}
	if err != nil {
		content = err.Error()
	}
	_, _ = event.Client().Rest.UpdateInteractionResponse(event.ApplicationID(), event.Token(),
		discord.MessageUpdate{Content: &content, Components: &components})
}

func (c *DisgoConnector) activateRepository(event *events.ComponentInteractionCreate, rawConversationID string) {
	if err := event.DeferUpdateMessage(); err != nil {
		return
	}
	conversationID, err := uuid.Parse(rawConversationID)
	data := event.StringSelectMenuInteractionData()
	var repositoryID uuid.UUID
	if err == nil && len(data.Values) == 1 {
		repositoryID, err = uuid.Parse(data.Values[0])
	} else if err == nil {
		err = errors.New("必须选择一个仓库")
	}
	profileID := uuid.Nil
	if err == nil {
		err = c.manager.db.QueryRowContext(context.Background(), "SELECT id FROM agent_profiles ORDER BY created_at LIMIT 1").Scan(&profileID)
	}
	if err == nil {
		err = c.conversations.Activate(context.Background(), conversationID, profileID, &repositoryID, event.User().ID.String())
	}
	content := "已选择仓库工作区，首条消息进入队列。"
	if err != nil {
		content = err.Error()
	}
	empty := []discord.LayoutComponent{}
	_, _ = event.Client().Rest.UpdateInteractionResponse(event.ApplicationID(), event.Token(),
		discord.MessageUpdate{Content: &content, Components: &empty})
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
			discord.ApplicationCommandOptionSubCommand{Name: "stop", Description: "停止当前会话的活动任务"},
		}},
	}
	_, err = client.Rest.SetGuildCommands(applicationID, guildID, commands, disgorest.WithCtx(ctx))
	return err
}

var _ GatewayConnector = (*DisgoConnector)(nil)
