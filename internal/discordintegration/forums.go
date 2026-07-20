package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"
)

var channelNamePart = regexp.MustCompile(`[^a-z0-9-]+`)

func (m *Manager) DevelopmentForumPlan(ctx context.Context, remoteGuild RemoteGuild,
	memberID string, repositoryID uuid.UUID, requestedName string,
) (InitializationPlan, error) {
	settings, err := m.Settings(ctx)
	if err != nil {
		return InitializationPlan{}, err
	}
	if settings.GuildID == "" || settings.BotUserID == "" {
		return InitializationPlan{}, errors.New("创建开发 Forum 前必须配置 Guild ID 和 Bot User ID")
	}
	var username, displayName, owner, repository string
	err = m.db.QueryRowContext(ctx, `SELECT m.username, m.display_name, r.owner, r.name
		FROM discord_members m CROSS JOIN repositories r
		JOIN discord_identity_bindings b ON b.guild_id = m.guild_id
			AND b.discord_user_id = m.discord_user_id AND b.status = 'active'
		WHERE m.guild_id = $1 AND m.discord_user_id = $2 AND m.active = true
			AND r.id = $3 AND r.enabled = true`, settings.GuildID, memberID, repositoryID).
		Scan(&username, &displayName, &owner, &repository)
	if err != nil {
		return InitializationPlan{}, errors.New("成员必须已绑定 GitHub，且仓库必须处于启用状态")
	}
	var allowed bool
	err = m.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM discord_forums f JOIN discord_forum_access a ON a.forum_id = f.id
		WHERE f.repository_id = $1 AND f.forum_type = 'repository'
			AND a.discord_user_id = $2 AND a.access_level = 'readonly')`, repositoryID, memberID).Scan(&allowed)
	if err != nil || !allowed {
		return InitializationPlan{}, errors.New("成员没有该仓库的 GitHub 读取权限")
	}
	categoryKey, categoryID, err := m.availableCodexCategory(ctx, settings.GuildID)
	if err != nil {
		return InitializationPlan{}, err
	}
	var desired []ChannelSpec
	if categoryID == "" {
		index, _ := strconv.Atoi(strings.TrimPrefix(categoryKey, "category.codex."))
		desired = append(desired, ChannelSpec{Key: categoryKey,
			Name: fmt.Sprintf("Codex 会话 %02d", index), Kind: "category"})
	}
	forumID := uuid.New()
	name := requestedName
	if name == "" {
		name = "dev-" + username + "-" + repository
	}
	name = channelNamePart.ReplaceAllString(strings.ToLower(name), "-")
	name = strings.Trim(name, "-")
	if name == "" {
		return InitializationPlan{}, errors.New("开发 Forum 名称无效")
	}
	key := "forum.development." + forumID.String()
	allow := discord.PermissionViewChannel | discord.PermissionSendMessages |
		discord.PermissionReadMessageHistory | discord.PermissionCreatePublicThreads |
		discord.PermissionSendMessagesInThreads | discord.PermissionAttachFiles | discord.PermissionEmbedLinks
	botAllow := allow | discord.PermissionManageChannels | discord.PermissionManageThreads | discord.PermissionManageMessages
	forum := ChannelSpec{Key: key, ParentKey: categoryKey, Name: name, Kind: "forum",
		Topic: "Tyrs Hand 长期开发环境 · " + displayName + " · " + owner + "/" + repository,
		PermissionOverwrites: []PermissionSpec{
			{ID: settings.GuildID, Type: "role", Deny: int64(discord.PermissionViewChannel)},
			{ID: memberID, Type: "member", Allow: int64(allow)},
			{ID: settings.BotUserID, Type: "member", Allow: int64(botAllow)},
		}}
	desired = append(desired, forum)
	managed, err := m.ManagedResources(ctx, settings.GuildID)
	if err != nil {
		return InitializationPlan{}, err
	}
	plan, err := BuildInitializationPlan(InitializationIncremental, remoteGuild, managed, desired)
	if err != nil {
		return InitializationPlan{}, err
	}
	plan.Actions = append(plan.Actions, InitializationAction{Kind: "forum.development.record",
		Spec: forum, OwnerUserID: memberID, RepositoryID: repositoryID.String(), ForumID: forumID.String()})
	return plan, nil
}

func (m *Manager) ServerInitializationPlan(ctx context.Context, remoteGuild RemoteGuild, mode string) (InitializationPlan, error) {
	settings, err := m.Settings(ctx)
	if err != nil {
		return InitializationPlan{}, err
	}
	if settings.GuildID == "" || settings.BotUserID == "" {
		return InitializationPlan{}, errors.New("初始化前必须配置 Guild ID 和 Bot User ID")
	}
	desired := BaseChannelSpecs()
	rows, err := m.db.QueryContext(ctx, `SELECT id::text, owner, name FROM repositories
		WHERE enabled = true ORDER BY lower(owner), lower(name)`)
	if err != nil {
		return InitializationPlan{}, err
	}
	var repositorySpecs []struct {
		id   string
		spec ChannelSpec
	}
	index := 0
	for rows.Next() {
		var repositoryID, owner, name string
		if err := rows.Scan(&repositoryID, &owner, &name); err != nil {
			_ = rows.Close()
			return InitializationPlan{}, err
		}
		shard := index/45 + 1
		categoryKey := fmt.Sprintf("category.github.%02d", shard)
		if shard > 1 && index%45 == 0 {
			desired = append(desired, ChannelSpec{Key: categoryKey, Name: fmt.Sprintf("GitHub 任务 %02d", shard), Kind: "category"})
		}
		channelName := channelNamePart.ReplaceAllString(strings.ToLower(owner+"-"+name), "-")
		key := "forum.repository." + repositoryID
		allowBot := discord.PermissionViewChannel | discord.PermissionReadMessageHistory |
			discord.PermissionManageChannels | discord.PermissionManageThreads | discord.PermissionManageMessages
		spec := ChannelSpec{Key: key, ParentKey: categoryKey, Name: channelName, Kind: "forum",
			Topic: "Tyrs Hand 只读任务看板 · " + owner + "/" + name,
			Tags:  []string{"Needs Attention", "Running", "Completed", "Failed"},
			PermissionOverwrites: []PermissionSpec{
				{ID: settings.GuildID, Type: "role", Deny: int64(discord.PermissionViewChannel)},
				{ID: settings.BotUserID, Type: "member", Allow: int64(allowBot)},
			},
		}
		desired = append(desired, spec)
		repositorySpecs = append(repositorySpecs, struct {
			id   string
			spec ChannelSpec
		}{id: repositoryID, spec: spec})
		index++
	}
	if err := rows.Close(); err != nil {
		return InitializationPlan{}, err
	}
	managed, err := m.ManagedResources(ctx, settings.GuildID)
	if err != nil {
		return InitializationPlan{}, err
	}
	plan, err := BuildInitializationPlan(mode, remoteGuild, managed, desired)
	if err != nil {
		return InitializationPlan{}, err
	}
	for _, repository := range repositorySpecs {
		plan.Actions = append(plan.Actions, InitializationAction{
			Kind: "forum.repository.record", Spec: repository.spec, RepositoryID: repository.id,
		})
	}
	return plan, nil
}

func (m *Manager) availableCodexCategory(ctx context.Context, guildID string) (string, string, error) {
	var key, id string
	err := m.db.QueryRowContext(ctx, `SELECT r.resource_key, r.discord_id
		FROM discord_resources r
		WHERE r.guild_id = $1 AND r.kind = 'category' AND r.resource_key LIKE 'category.codex.%'
			AND (SELECT count(*) FROM discord_resources child
				WHERE child.guild_id = r.guild_id AND child.parent_discord_id = r.discord_id AND child.status = 'active') < 45
		ORDER BY r.resource_key LIMIT 1`, guildID).Scan(&key, &id)
	if err == nil {
		return key, id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	var count int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM discord_resources
		WHERE guild_id = $1 AND resource_key LIKE 'category.codex.%'`, guildID).Scan(&count); err != nil {
		return "", "", err
	}
	return fmt.Sprintf("category.codex.%02d", count+1), "", nil
}

func (m *Manager) SetForumAccess(ctx context.Context, forumID uuid.UUID, memberID, level string, administratorID uuid.UUID) error {
	if level != AccessReadOnly && level != AccessOperator {
		return errors.New("forum 权限必须是 readonly 或 operator")
	}
	_, err := m.db.ExecContext(ctx, `INSERT INTO discord_forum_access
		(forum_id, discord_user_id, access_level, granted_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT(forum_id, discord_user_id) DO UPDATE SET access_level = EXCLUDED.access_level,
			granted_by = EXCLUDED.granted_by, updated_at = now()`, forumID, memberID, level, administratorID)
	if err != nil {
		return err
	}
	return m.syncForumPermissions(ctx, forumID)
}

func (m *Manager) DeleteForumAccess(ctx context.Context, forumID uuid.UUID, memberID string) error {
	_, err := m.db.ExecContext(ctx, `DELETE FROM discord_forum_access WHERE forum_id = $1 AND discord_user_id = $2`, forumID, memberID)
	if err != nil {
		return err
	}
	return m.syncForumPermissions(ctx, forumID)
}

func (m *Manager) syncForumPermissions(ctx context.Context, forumID uuid.UUID) error {
	var guildID, channelID, ownerID, botID string
	err := m.db.QueryRowContext(ctx, `SELECT f.guild_id, r.discord_id, f.owner_discord_user_id,
		COALESCE(g.bot_user_id, '') FROM discord_forums f JOIN discord_resources r ON r.id = f.resource_id
		JOIN discord_guilds g ON g.guild_id = f.guild_id WHERE f.id = $1 AND f.forum_type = 'development'`, forumID).
		Scan(&guildID, &channelID, &ownerID, &botID)
	if err != nil {
		return err
	}
	viewRead := discord.PermissionViewChannel | discord.PermissionReadMessageHistory
	operate := viewRead | discord.PermissionSendMessages | discord.PermissionCreatePublicThreads |
		discord.PermissionSendMessagesInThreads | discord.PermissionAttachFiles | discord.PermissionEmbedLinks
	permissions := []PermissionSpec{
		{ID: guildID, Type: "role", Deny: int64(discord.PermissionViewChannel)},
		{ID: ownerID, Type: "member", Allow: int64(operate)},
		{ID: botID, Type: "member", Allow: int64(operate | discord.PermissionManageChannels | discord.PermissionManageThreads | discord.PermissionManageMessages)},
	}
	rows, err := m.db.QueryContext(ctx, `SELECT discord_user_id, access_level FROM discord_forum_access
		WHERE forum_id = $1 ORDER BY discord_user_id`, forumID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var memberID, level string
		if err := rows.Scan(&memberID, &level); err != nil {
			_ = rows.Close()
			return err
		}
		permission := PermissionSpec{ID: memberID, Type: "member", Allow: int64(viewRead)}
		if level == AccessOperator {
			permission.Allow = int64(operate)
		} else {
			permission.Deny = int64(discord.PermissionSendMessages | discord.PermissionCreatePublicThreads | discord.PermissionSendMessagesInThreads)
		}
		permissions = append(permissions, permission)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return NewSQLoutbox(m.db).Enqueue(ctx, "forum-permissions:"+forumID.String(), "channel.permissions",
		"channels/"+channelID, map[string]any{"channelId": channelID, "permissions": permissions}, "")
}
