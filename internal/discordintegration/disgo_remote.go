package discordintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/disgoorg/disgo/discord"
	disgorest "github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"
)

type DisgoRemote struct {
	rest disgorest.Rest
}

func NewDisgoRemote(token, apiURL string, httpClient *http.Client) *DisgoRemote {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	options := []disgorest.ClientConfigOpt{
		disgorest.WithHTTPClient(httpClient),
		disgorest.WithUserAgent("Tyrs-Hand/discord-v1"),
		disgorest.WithRateLimiterConfigOpts(disgorest.WithMaxRetries(3)),
	}
	if apiURL != "" {
		options = append(options, disgorest.WithURL(strings.TrimRight(apiURL, "/")))
	}
	client := disgorest.NewClient(token, options...)
	return &DisgoRemote{rest: disgorest.New(client, disgorest.WithDefaultAllowedMentions(discord.AllowedMentions{}))}
}

func (r *DisgoRemote) Guild(ctx context.Context, guildID string) (RemoteGuild, error) {
	id, err := snowflake.Parse(guildID)
	if err != nil {
		return RemoteGuild{}, err
	}
	guild, err := r.rest.GetGuild(id, false, disgorest.WithCtx(ctx))
	if err != nil {
		return RemoteGuild{}, err
	}
	channels, err := r.rest.GetGuildChannels(id, disgorest.WithCtx(ctx))
	if err != nil {
		return RemoteGuild{}, err
	}
	result := RemoteGuild{ID: guildID, Name: guild.Name, CommunityEnabled: slices.Contains(guild.Features, discord.GuildFeatureCommunity)}
	for _, channel := range channels {
		value := RemoteChannel{ID: channel.ID().String(), Name: channel.Name(), Kind: channelKind(channel)}
		if parent := channel.ParentID(); parent != nil {
			value.ParentID = parent.String()
		}
		value.Topic = channelTopic(channel)
		value.Tags = channelTags(channel)
		result.Channels = append(result.Channels, value)
	}
	return result, nil
}

func (r *DisgoRemote) DisableCommunity(ctx context.Context, guildID string) error {
	id, err := snowflake.Parse(guildID)
	if err != nil {
		return err
	}
	guild, err := r.rest.GetGuild(id, false, disgorest.WithCtx(ctx))
	if err != nil {
		return err
	}
	features := slices.DeleteFunc(slices.Clone(guild.Features), func(feature discord.GuildFeature) bool {
		return feature == discord.GuildFeatureCommunity
	})
	_, err = r.rest.UpdateGuild(id, discord.GuildUpdate{Features: &features}, disgorest.WithCtx(ctx))
	return err
}

func (r *DisgoRemote) EnableCommunity(ctx context.Context, guildID, rulesChannelID, updatesChannelID string) error {
	guild, err := snowflake.Parse(guildID)
	if err != nil {
		return err
	}
	rules, err := snowflake.Parse(rulesChannelID)
	if err != nil {
		return err
	}
	updates, err := snowflake.Parse(updatesChannelID)
	if err != nil {
		return err
	}
	features := []discord.GuildFeature{discord.GuildFeatureCommunity}
	verificationLevel := discord.VerificationLevelLow
	contentFilter := discord.ExplicitContentFilterLevelAllMembers
	_, err = r.rest.UpdateGuild(guild, discord.GuildUpdate{
		Features: &features, RulesChannelID: &rules, PublicUpdatesChannelID: &updates,
		VerificationLevel:     omit.New(&verificationLevel),
		ExplicitContentFilter: omit.New(&contentFilter),
	}, disgorest.WithCtx(ctx))
	return err
}

func (r *DisgoRemote) CreateChannel(ctx context.Context, guildID string, spec ChannelSpec, marker string) (RemoteChannel, error) {
	guild, err := snowflake.Parse(guildID)
	if err != nil {
		return RemoteChannel{}, err
	}
	parent, err := optionalSnowflake(spec.ParentKey)
	if err != nil {
		return RemoteChannel{}, err
	}
	topic := managedTopic(spec.Topic, marker)
	overwrites, err := permissionOverwrites(spec.PermissionOverwrites)
	if err != nil {
		return RemoteChannel{}, err
	}
	var create discord.GuildChannelCreate
	switch spec.Kind {
	case "category":
		create = discord.GuildCategoryChannelCreate{Name: spec.Name, PermissionOverwrites: overwrites}
	case "text":
		create = discord.GuildTextChannelCreate{Name: spec.Name, Topic: topic, ParentID: parent, PermissionOverwrites: overwrites}
	case "forum":
		tags := make([]discord.ChannelTag, 0, len(spec.Tags))
		for _, name := range spec.Tags {
			tags = append(tags, discord.ChannelTag{Name: name})
		}
		create = discord.GuildForumChannelCreate{Name: spec.Name, Topic: topic, ParentID: parent, PermissionOverwrites: overwrites,
			DefaultForumLayout: discord.DefaultForumLayoutListView, AvailableTags: tags}
	default:
		return RemoteChannel{}, fmt.Errorf("不支持的 Discord Channel 类型 %q", spec.Kind)
	}
	channel, err := r.rest.CreateGuildChannel(guild, create, disgorest.WithCtx(ctx))
	if err != nil {
		return RemoteChannel{}, err
	}
	parentID := ""
	if parent != 0 {
		parentID = parent.String()
	}
	return RemoteChannel{ID: channel.ID().String(), Name: channel.Name(), Kind: spec.Kind,
		ParentID: parentID, Topic: topic}, nil
}

func (r *DisgoRemote) UpdateChannel(ctx context.Context, channelID string, spec ChannelSpec) error {
	id, err := snowflake.Parse(channelID)
	if err != nil {
		return err
	}
	parent, err := optionalSnowflake(spec.ParentKey)
	if err != nil {
		return err
	}
	name, topic := spec.Name, spec.Topic
	overwrites, err := permissionOverwrites(spec.PermissionOverwrites)
	if err != nil {
		return err
	}
	var overwriteUpdate *[]discord.PermissionOverwrite
	if spec.PermissionOverwrites != nil {
		overwriteUpdate = &overwrites
	}
	var update discord.ChannelUpdate
	switch spec.Kind {
	case "category":
		update = discord.GuildCategoryChannelUpdate{Name: &name, PermissionOverwrites: overwriteUpdate}
	case "text":
		update = discord.GuildTextChannelUpdate{Name: &name, Topic: &topic, ParentID: &parent, PermissionOverwrites: overwriteUpdate}
	case "forum":
		update = discord.GuildForumChannelUpdate{Name: &name, Topic: &topic, ParentID: &parent, PermissionOverwrites: overwriteUpdate}
	default:
		return fmt.Errorf("不支持的 Discord Channel 类型 %q", spec.Kind)
	}
	_, err = r.rest.UpdateChannel(id, update, disgorest.WithCtx(ctx))
	return err
}

func (r *DisgoRemote) DeleteChannel(ctx context.Context, channelID string) error {
	id, err := snowflake.Parse(channelID)
	if err != nil {
		return err
	}
	return r.rest.DeleteChannel(id, disgorest.WithCtx(ctx))
}

func (r *DisgoRemote) Send(ctx context.Context, item OutboxItem) (json.RawMessage, error) {
	item.Nonce = discordNonce(item.Nonce)
	var payload struct {
		ChannelID        string                `json:"channelId"`
		MessageID        string                `json:"messageId"`
		UserID           string                `json:"userId"`
		Content          string                `json:"content"`
		InteractionID    string                `json:"interactionId"`
		InteractionToken string                `json:"interactionToken"`
		Ephemeral        bool                  `json:"ephemeral"`
		Permissions      []PermissionSpec      `json:"permissions"`
		ThreadName       string                `json:"threadName"`
		TagIDs           []string              `json:"tagIds"`
		Archived         bool                  `json:"archived"`
		Locked           bool                  `json:"locked"`
		ConversationID   string                `json:"conversationId"`
		Card             *ComponentCardPayload `json:"card"`
	}
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return nil, err
	}
	switch item.OperationType {
	case "channel.delete":
		channel, err := snowflake.Parse(payload.ChannelID)
		if err != nil {
			return nil, err
		}
		err = r.rest.DeleteChannel(channel, disgorest.WithCtx(ctx))
		return json.RawMessage(`{}`), err
	case "message.create":
		channel, err := snowflake.Parse(payload.ChannelID)
		if err != nil {
			return nil, err
		}
		create := discord.MessageCreate{Content: payload.Content, Nonce: item.Nonce,
			EnforceNonce: item.Nonce != "", AllowedMentions: &discord.AllowedMentions{}}
		if payload.Card != nil {
			components, componentErr := discordCardComponents(*payload.Card)
			if componentErr != nil {
				return nil, componentErr
			}
			create = discord.NewMessageCreateV2(components...)
			create.Nonce, create.EnforceNonce = item.Nonce, item.Nonce != ""
			create.AllowedMentions = &discord.AllowedMentions{}
		}
		message, err := r.rest.CreateMessage(channel, create, disgorest.WithCtx(ctx))
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]string{"messageId": message.ID.String()})
	case "message.update":
		channel, message, err := twoSnowflakes(payload.ChannelID, payload.MessageID)
		if err != nil {
			return nil, err
		}
		emptyComponents := []discord.LayoutComponent{}
		update := discord.MessageUpdate{Content: &payload.Content, Components: &emptyComponents,
			AllowedMentions: &discord.AllowedMentions{}}
		if payload.Card != nil {
			components, componentErr := discordCardComponents(*payload.Card)
			if componentErr != nil {
				return nil, componentErr
			}
			update = discord.NewMessageUpdateV2(components...)
			emptyContent := ""
			emptyEmbeds := []discord.Embed{}
			update.Content, update.Embeds = &emptyContent, &emptyEmbeds
			update.AllowedMentions = &discord.AllowedMentions{}
		}
		_, err = r.rest.UpdateMessage(channel, message, update, disgorest.WithCtx(ctx))
		if discordThreadArchived(err) {
			// 隐藏或真正归档的 Thread 不能更新消息；恢复后的自然投影会再次刷新内容。
			err = nil
		}
		return json.RawMessage(`{}`), err
	case "message.delete":
		channel, message, err := twoSnowflakes(payload.ChannelID, payload.MessageID)
		if err != nil {
			return nil, err
		}
		err = r.rest.DeleteMessage(channel, message, disgorest.WithCtx(ctx))
		if discordNotFound(err) {
			err = nil
		}
		return json.RawMessage(`{}`), err
	case "interaction.defer":
		interaction, err := snowflake.Parse(payload.InteractionID)
		if err != nil {
			return nil, err
		}
		message := discord.MessageCreate{}
		if payload.Ephemeral {
			message = message.WithEphemeral(true)
		}
		response := discord.InteractionResponse{Type: discord.InteractionResponseTypeDeferredCreateMessage, Data: message}
		return nil, r.rest.CreateInteractionResponse(interaction, payload.InteractionToken, response, disgorest.WithCtx(ctx))
	case "channel.permissions":
		channel, err := snowflake.Parse(payload.ChannelID)
		if err != nil {
			return nil, err
		}
		overwrites, err := permissionOverwrites(payload.Permissions)
		if err != nil {
			return nil, err
		}
		_, err = r.rest.UpdateChannel(channel, discord.GuildForumChannelUpdate{PermissionOverwrites: &overwrites}, disgorest.WithCtx(ctx))
		return nil, err
	case "forum.post.create":
		forum, err := snowflake.Parse(payload.ChannelID)
		if err != nil {
			return nil, err
		}
		tags := make([]snowflake.ID, 0, len(payload.TagIDs))
		for _, rawID := range payload.TagIDs {
			id, parseErr := snowflake.Parse(rawID)
			if parseErr != nil {
				return nil, parseErr
			}
			tags = append(tags, id)
		}
		message := discord.MessageCreate{Content: payload.Content, Nonce: item.Nonce,
			EnforceNonce: item.Nonce != "", AllowedMentions: &discord.AllowedMentions{}}
		if payload.Card != nil {
			components, componentErr := discordCardComponents(*payload.Card)
			if componentErr != nil {
				return nil, componentErr
			}
			message = discord.NewMessageCreateV2(components...)
			message.Nonce, message.EnforceNonce = item.Nonce, item.Nonce != ""
			message.AllowedMentions = &discord.AllowedMentions{}
		}
		post, err := r.rest.CreatePostInThreadChannel(forum, discord.ThreadChannelPostCreate{
			Name: payload.ThreadName, AutoArchiveDuration: discord.AutoArchiveDuration1w,
			AppliedTags: tags, Message: message,
		}, disgorest.WithCtx(ctx))
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]string{"threadId": post.ID().String(), "messageId": post.Message.ID.String()})
	case "thread.archive":
		thread, err := snowflake.Parse(payload.ChannelID)
		if err != nil {
			return nil, err
		}
		_, err = r.rest.UpdateChannel(thread, discord.GuildPostUpdate{Archived: &payload.Archived}, disgorest.WithCtx(ctx))
		return nil, err
	case "thread.lifecycle":
		thread, err := snowflake.Parse(payload.ChannelID)
		if err != nil {
			return nil, err
		}
		_, err = r.rest.UpdateChannel(thread, discord.GuildPostUpdate{
			Archived: &payload.Archived,
			Locked:   &payload.Locked,
		}, disgorest.WithCtx(ctx))
		return nil, err
	case "thread.member.add":
		thread, user, err := twoSnowflakes(payload.ChannelID, payload.UserID)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), r.rest.AddThreadMember(thread, user,
			disgorest.WithCtx(ctx))
	case "thread.rename":
		thread, err := snowflake.Parse(payload.ChannelID)
		if err != nil {
			return nil, err
		}
		name := payload.ThreadName
		_, err = r.rest.UpdateChannel(thread, discord.GuildPostUpdate{Name: &name}, disgorest.WithCtx(ctx))
		return nil, err
	case "thread.tags":
		thread, err := snowflake.Parse(payload.ChannelID)
		if err != nil {
			return nil, err
		}
		tags := make([]snowflake.ID, 0, len(payload.TagIDs))
		for _, rawID := range payload.TagIDs {
			id, parseErr := snowflake.Parse(rawID)
			if parseErr != nil {
				return nil, parseErr
			}
			tags = append(tags, id)
		}
		_, err = r.rest.UpdateChannel(thread, discord.GuildPostUpdate{AppliedTags: &tags}, disgorest.WithCtx(ctx))
		return nil, err
	default:
		return nil, fmt.Errorf("不支持的 Discord Outbox 操作 %q", item.OperationType)
	}
}

func discordNotFound(err error) bool {
	var restErr *disgorest.Error
	return errors.As(err, &restErr) && restErr.Response != nil &&
		restErr.Response.StatusCode == http.StatusNotFound
}

func discordThreadArchived(err error) bool {
	var restErr *disgorest.Error
	return errors.As(err, &restErr) &&
		restErr.Code == disgorest.JSONErrorCodeOperationOnArchivedThread
}

func (r *DisgoRemote) Close(ctx context.Context) { r.rest.Close(ctx) }

func discordCardComponents(card ComponentCardPayload) ([]discord.LayoutComponent, error) {
	if card.AccentColor < 0 || card.AccentColor > 0xFFFFFF || strings.TrimSpace(card.Header) == "" {
		return nil, fmt.Errorf("discord Components V2 卡片无效")
	}
	parts := make([]discord.ContainerSubComponent, 0, 9)
	addText := func(value string) error {
		if strings.TrimSpace(value) == "" {
			return nil
		}
		if utf8.RuneCountInString(value) > 4000 {
			return fmt.Errorf("discord Text Display 超过 4000 字符")
		}
		if len(parts) > 0 {
			parts = append(parts, discord.NewSmallSeparator())
		}
		parts = append(parts, discord.NewTextDisplay(value))
		return nil
	}
	for _, value := range []string{card.Header, card.Body, card.Timeline, card.Footer} {
		if err := addText(value); err != nil {
			return nil, err
		}
	}
	if len(card.Buttons) > 0 {
		if len(card.Buttons) > 5 {
			return nil, fmt.Errorf("discord Action Row 最多包含 5 个按钮")
		}
		buttons := make([]discord.InteractiveComponent, 0, len(card.Buttons))
		seen := make(map[string]bool, len(card.Buttons))
		for _, button := range card.Buttons {
			if button.CustomID == "" || len(button.CustomID) > 100 || seen[button.CustomID] ||
				utf8.RuneCountInString(button.Label) == 0 || utf8.RuneCountInString(button.Label) > 80 {
				return nil, fmt.Errorf("discord 按钮 custom_id 无效或重复")
			}
			seen[button.CustomID] = true
			component := discord.NewSecondaryButton(button.Label, button.CustomID)
			if button.Style == "primary" {
				component = discord.NewPrimaryButton(button.Label, button.CustomID)
			}
			component.Disabled = button.Disabled
			buttons = append(buttons, component)
		}
		parts = append(parts, discord.NewSmallSeparator(), discord.NewActionRow(buttons...))
	}
	container := discord.NewContainer(parts...).WithAccentColor(card.AccentColor)
	return []discord.LayoutComponent{container}, nil
}

func optionalSnowflake(value string) (snowflake.ID, error) {
	if value == "" {
		return 0, nil
	}
	return snowflake.Parse(value)
}

func permissionOverwrites(specs []PermissionSpec) ([]discord.PermissionOverwrite, error) {
	result := make([]discord.PermissionOverwrite, 0, len(specs))
	for _, spec := range specs {
		id, err := snowflake.Parse(spec.ID)
		if err != nil {
			return nil, err
		}
		switch spec.Type {
		case "role":
			result = append(result, discord.RolePermissionOverwrite{RoleID: id,
				Allow: discord.Permissions(spec.Allow), Deny: discord.Permissions(spec.Deny)})
		case "member":
			result = append(result, discord.MemberPermissionOverwrite{UserID: id,
				Allow: discord.Permissions(spec.Allow), Deny: discord.Permissions(spec.Deny)})
		default:
			return nil, fmt.Errorf("未知 Discord Permission Overwrite 类型 %q", spec.Type)
		}
	}
	return result, nil
}

func twoSnowflakes(first, second string) (snowflake.ID, snowflake.ID, error) {
	a, err := snowflake.Parse(first)
	if err != nil {
		return 0, 0, err
	}
	b, err := snowflake.Parse(second)
	return a, b, err
}

func managedTopic(topic, marker string) string {
	if marker == "" {
		return topic
	}
	for line := range strings.SplitSeq(topic, "\n") {
		if strings.TrimSpace(line) == marker {
			return topic
		}
	}
	if topic != "" {
		return topic + "\n" + marker
	}
	return marker
}

func channelKind(channel discord.GuildChannel) string {
	switch channel.Type() {
	case discord.ChannelTypeGuildCategory:
		return "category"
	case discord.ChannelTypeGuildForum:
		return "forum"
	case discord.ChannelTypeGuildText:
		return "text"
	default:
		return "unsupported"
	}
}

func channelTopic(channel discord.GuildChannel) string {
	switch value := channel.(type) {
	case discord.GuildTextChannel:
		return stringValue(value.Topic())
	case *discord.GuildTextChannel:
		return stringValue(value.Topic())
	case discord.GuildForumChannel:
		return stringValue(value.Topic)
	case *discord.GuildForumChannel:
		return stringValue(value.Topic)
	default:
		return ""
	}
}

func channelTags(channel discord.GuildChannel) map[string]string {
	result := map[string]string{}
	var tags []discord.ChannelTag
	switch value := channel.(type) {
	case discord.GuildForumChannel:
		tags = value.AvailableTags
	case *discord.GuildForumChannel:
		tags = value.AvailableTags
	}
	for _, tag := range tags {
		result[tag.Name] = tag.ID.String()
	}
	return result
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var _ Remote = (*DisgoRemote)(nil)
