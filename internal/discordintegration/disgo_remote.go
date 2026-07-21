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
		ChannelID        string           `json:"channelId"`
		MessageID        string           `json:"messageId"`
		Content          string           `json:"content"`
		InteractionID    string           `json:"interactionId"`
		InteractionToken string           `json:"interactionToken"`
		Ephemeral        bool             `json:"ephemeral"`
		Permissions      []PermissionSpec `json:"permissions"`
		ThreadName       string           `json:"threadName"`
		TagIDs           []string         `json:"tagIds"`
		Archived         bool             `json:"archived"`
		ConversationID   string           `json:"conversationId"`
		Embeds           *[]EmbedPayload  `json:"embeds"`
		Buttons          []struct {
			Label    string `json:"label"`
			CustomID string `json:"customId"`
			Style    string `json:"style"`
		} `json:"buttons"`
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
		create := discord.MessageCreate{
			Content: payload.Content, Nonce: item.Nonce, EnforceNonce: item.Nonce != "",
		}
		if payload.Embeds != nil {
			embeds, embedErr := discordEmbeds(*payload.Embeds)
			if embedErr != nil {
				return nil, embedErr
			}
			create.Embeds = embeds
		}
		if len(payload.Buttons) > 0 {
			buttons := make([]discord.InteractiveComponent, 0, len(payload.Buttons))
			for _, button := range payload.Buttons {
				component := discord.NewSecondaryButton(button.Label, button.CustomID)
				if button.Style == "primary" {
					component = discord.NewPrimaryButton(button.Label, button.CustomID)
				}
				buttons = append(buttons, component)
			}
			create.Components = []discord.LayoutComponent{discord.NewActionRow(buttons...)}
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
		update := discord.MessageUpdate{Content: &payload.Content, Components: &emptyComponents}
		if payload.Embeds != nil {
			embeds, embedErr := discordEmbeds(*payload.Embeds)
			if embedErr != nil {
				return nil, embedErr
			}
			update.Embeds = &embeds
		}
		if len(payload.Buttons) > 0 {
			buttons := make([]discord.InteractiveComponent, 0, len(payload.Buttons))
			for _, button := range payload.Buttons {
				component := discord.NewSecondaryButton(button.Label, button.CustomID)
				if button.Style == "primary" {
					component = discord.NewPrimaryButton(button.Label, button.CustomID)
				}
				buttons = append(buttons, component)
			}
			components := []discord.LayoutComponent{discord.NewActionRow(buttons...)}
			update.Components = &components
		}
		_, err = r.rest.UpdateMessage(channel, message, update, disgorest.WithCtx(ctx))
		return nil, err
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
		embeds := []discord.Embed(nil)
		if payload.Embeds != nil {
			embeds, err = discordEmbeds(*payload.Embeds)
			if err != nil {
				return nil, err
			}
		}
		post, err := r.rest.CreatePostInThreadChannel(forum, discord.ThreadChannelPostCreate{
			Name: payload.ThreadName, AutoArchiveDuration: discord.AutoArchiveDuration1w,
			AppliedTags: tags, Message: discord.MessageCreate{Content: payload.Content, Embeds: embeds,
				Nonce: item.Nonce, EnforceNonce: item.Nonce != ""},
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

func (r *DisgoRemote) Close(ctx context.Context) { r.rest.Close(ctx) }

func discordEmbeds(values []EmbedPayload) ([]discord.Embed, error) {
	if len(values) > 10 {
		return nil, errors.New("discord 消息最多包含 10 个 Embed")
	}
	result := make([]discord.Embed, 0, len(values))
	globalTotal := 0
	for _, value := range values {
		if value.Color < 0 || value.Color > 0xFFFFFF {
			return nil, errors.New("discord Embed 颜色超出范围")
		}
		if utf8.RuneCountInString(value.Title) > 256 || utf8.RuneCountInString(value.Description) > 4096 ||
			utf8.RuneCountInString(value.Footer) > 2048 || len(value.Fields) > 25 {
			return nil, errors.New("discord Embed 内容超出长度限制")
		}
		total := utf8.RuneCountInString(value.Title) + utf8.RuneCountInString(value.Description) + utf8.RuneCountInString(value.Footer)
		if total == 0 && len(value.Fields) == 0 {
			return nil, errors.New("discord Embed 不能为空")
		}
		embed := discord.Embed{Title: value.Title, Description: value.Description, Color: value.Color}
		if value.Footer != "" {
			embed.Footer = &discord.EmbedFooter{Text: value.Footer}
		}
		if value.Timestamp != "" {
			parsed, err := time.Parse(time.RFC3339, value.Timestamp)
			if err != nil {
				return nil, errors.New("discord Embed 时间格式无效")
			}
			embed.Timestamp = &parsed
		}
		for _, field := range value.Fields {
			if field.Name == "" || field.Value == "" || utf8.RuneCountInString(field.Name) > 256 || utf8.RuneCountInString(field.Value) > 1024 {
				return nil, errors.New("discord Embed Field 内容无效")
			}
			total += utf8.RuneCountInString(field.Name) + utf8.RuneCountInString(field.Value)
			inline := field.Inline
			embed.Fields = append(embed.Fields, discord.EmbedField{Name: field.Name, Value: field.Value, Inline: &inline})
		}
		globalTotal += total
		if globalTotal > 6000 {
			return nil, errors.New("discord Embed 总长度超出限制")
		}
		result = append(result, embed)
	}
	return result, nil
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
