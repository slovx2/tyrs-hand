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

func (m *Manager) WorkspaceProjectForumPlan(ctx context.Context, remoteGuild RemoteGuild,
	projectID uuid.UUID, requestedName string,
) (InitializationPlan, error) {
	settings, err := m.Settings(ctx)
	if err != nil {
		return InitializationPlan{}, err
	}
	if settings.GuildID == "" || settings.BotUserID == "" {
		return InitializationPlan{}, errors.New("创建开发 Forum 前必须配置 Guild ID 和 Bot User ID")
	}
	var memberID, username, displayName, projectName string
	err = m.db.QueryRowContext(ctx, `SELECT workspace.owner_discord_user_id,
		member.username, member.display_name, project.name
		FROM workspace_projects project
		JOIN worker_workspaces workspace ON workspace.id=project.workspace_id
		JOIN discord_members member ON member.guild_id=workspace.guild_id
			AND member.discord_user_id=workspace.owner_discord_user_id
		WHERE project.id=$1 AND workspace.guild_id=$2 AND member.active
		  AND project.availability_status='available'
		  AND NOT EXISTS (
			SELECT 1 FROM discord_forums forum
			WHERE forum.workspace_project_id=project.id
			  AND forum.binding_status='active')`, projectID, settings.GuildID).
		Scan(&memberID, &username, &displayName, &projectName)
	if err != nil {
		return InitializationPlan{}, errors.New("项目不可用、环境未就绪或已经存在活跃 Forum")
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
	name := workspaceProjectForumName(remoteGuild, requestedName, displayName, username,
		projectName, forumID)
	if name == "" {
		return InitializationPlan{}, errors.New("Workspace Forum 名称无效")
	}
	key := "forum.workspace." + forumID.String()
	allow := discord.PermissionViewChannel | discord.PermissionSendMessages |
		discord.PermissionReadMessageHistory | discord.PermissionCreatePublicThreads |
		discord.PermissionSendMessagesInThreads | discord.PermissionAttachFiles | discord.PermissionEmbedLinks
	botAllow := allow | discord.PermissionManageChannels | discord.PermissionManageThreads | discord.PermissionManageMessages
	forum := ChannelSpec{Key: key, ParentKey: categoryKey, Name: name, Kind: "forum",
		Topic: "Tyrs Hand Workspace · " + displayName + " · " + projectName,
		Tags:  []string{"Running"},
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
	plan.Actions = append(plan.Actions, InitializationAction{Kind: "forum.workspace_project.record",
		Spec: forum, OwnerUserID: memberID, ProjectID: projectID.String(),
		ForumID: forumID.String()})
	return plan, nil
}

func workspaceProjectForumName(guild RemoteGuild, requestedName, displayName, username,
	project string, forumID uuid.UUID,
) string {
	if strings.TrimSpace(requestedName) != "" {
		return channelName(requestedName)
	}
	member := displayName
	if strings.TrimSpace(member) == "" {
		member = username
	}
	base := channelName(member + "-" + project)
	for _, channel := range guild.Channels {
		if channelName(channel.Name) == base {
			suffix := strings.ReplaceAll(forumID.String(), "-", "")[:6]
			return channelName(base + "-" + suffix)
		}
	}
	return base
}

func (m *Manager) RestoreWorkspaceForum(ctx context.Context, projectID,
	forumID uuid.UUID,
) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE discord_forums forum
		SET binding_status='active'
		FROM workspace_projects project
		JOIN worker_workspaces workspace ON workspace.id=project.workspace_id
		WHERE forum.id=$2 AND forum.workspace_project_id=project.id
		  AND project.id=$1 AND forum.forum_type='workspace'
		  AND forum.binding_status='inactive'
		  AND project.availability_status='available'
		  AND NOT EXISTS (
			SELECT 1 FROM discord_forums active
			WHERE active.workspace_project_id=project.id
			  AND active.binding_status='active')`, projectID, forumID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("无法恢复 Forum：项目缺失或已经存在活跃 Forum")
	}
	if err := syncForumPermissions(ctx, tx, forumID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) DisableWorkspaceForum(ctx context.Context, forumID uuid.UUID) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE discord_forums
		SET binding_status='inactive'
		WHERE id=$1 AND forum_type='workspace' AND binding_status='active'`, forumID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("活跃开发 Forum 不存在")
	}
	if err := syncForumPermissions(ctx, tx, forumID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) EnableWorkspaceForum(ctx context.Context, forumID uuid.UUID) error {
	var projectID uuid.UUID
	if err := m.db.QueryRowContext(ctx, `SELECT workspace_project_id
		FROM discord_forums WHERE id=$1 AND forum_type='workspace'`, forumID).
		Scan(&projectID); err != nil {
		return errors.New("开发 Forum 不存在")
	}
	return m.RestoreWorkspaceForum(ctx, projectID, forumID)
}

func (m *Manager) ForumAccess(ctx context.Context, forumID uuid.UUID) ([]ForumAccess, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT forum_id::text, discord_user_id, access_level
		FROM discord_forum_access WHERE forum_id=$1 ORDER BY discord_user_id`, forumID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]ForumAccess, 0)
	for rows.Next() {
		var item ForumAccess
		if err := rows.Scan(&item.ForumID, &item.MemberID, &item.AccessLevel); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (m *Manager) SetWorkspaceProjectForumAccess(ctx context.Context, projectID,
	forumID uuid.UUID, memberID, level string, administratorID uuid.UUID,
) error {
	var matches bool
	if err := m.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM discord_forums
		WHERE id=$2 AND workspace_project_id=$1 AND forum_type='workspace')`,
		projectID, forumID).Scan(&matches); err != nil || !matches {
		return errors.New("项目 Forum 不存在")
	}
	return m.SetForumAccess(ctx, forumID, memberID, level, administratorID)
}

func (m *Manager) DeleteWorkspaceProjectForumAccess(ctx context.Context, projectID,
	forumID uuid.UUID, memberID string,
) error {
	var matches bool
	if err := m.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM discord_forums
		WHERE id=$2 AND workspace_project_id=$1 AND forum_type='workspace')`,
		projectID, forumID).Scan(&matches); err != nil || !matches {
		return errors.New("项目 Forum 不存在")
	}
	return m.DeleteForumAccess(ctx, forumID, memberID)
}

func workspaceForumName(guild RemoteGuild, requestedName, displayName, username,
	owner, repository string, forumID uuid.UUID,
) string {
	if strings.TrimSpace(requestedName) != "" {
		return channelName(requestedName)
	}
	member := channelName(displayName)
	if member == "" {
		member = channelName(username)
	}
	base := channelName(member + "-" + repository)
	ownerScoped := channelName(member + "-" + owner + "-" + repository)
	used := make(map[string]bool, len(guild.Channels))
	for _, channel := range guild.Channels {
		used[channelName(channel.Name)] = true
	}
	if base != "" && !used[base] {
		return base
	}
	if ownerScoped != "" && !used[ownerScoped] {
		return ownerScoped
	}
	suffix := strings.ReplaceAll(forumID.String(), "-", "")[:6]
	return channelName(ownerScoped + "-" + suffix)
}

func channelName(value string) string {
	value = channelNamePart.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-")
	return strings.Trim(value, "-")
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
		repositoryChannelName := channelName(owner + "-" + name)
		key := "forum.repository." + repositoryID
		allowBot := discord.PermissionViewChannel | discord.PermissionReadMessageHistory |
			discord.PermissionManageChannels | discord.PermissionManageThreads | discord.PermissionManageMessages
		spec := ChannelSpec{Key: key, ParentKey: categoryKey, Name: repositoryChannelName, Kind: "forum",
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
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO discord_forum_access
		(forum_id, discord_user_id, access_level, granted_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT(forum_id, discord_user_id) DO UPDATE SET access_level = EXCLUDED.access_level,
			granted_by = EXCLUDED.granted_by, updated_at = now()`, forumID, memberID, level, administratorID)
	if err != nil {
		return err
	}
	if err := syncForumPermissions(ctx, tx, forumID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) DeleteForumAccess(ctx context.Context, forumID uuid.UUID, memberID string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `DELETE FROM discord_forum_access
		WHERE forum_id = $1 AND discord_user_id = $2`, forumID, memberID)
	if err != nil {
		return err
	}
	if err := syncForumPermissions(ctx, tx, forumID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) syncForumPermissions(ctx context.Context, forumID uuid.UUID) error {
	return syncForumPermissions(ctx, m.db, forumID)
}

type forumPermissionStore interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func syncForumPermissions(ctx context.Context, store forumPermissionStore,
	forumID uuid.UUID,
) error {
	var guildID, channelID, ownerID, botID, bindingStatus string
	err := store.QueryRowContext(ctx, `SELECT f.guild_id, r.discord_id, f.owner_discord_user_id,
		COALESCE(g.bot_user_id, ''), f.binding_status
		FROM discord_forums f JOIN discord_resources r ON r.id = f.resource_id
		JOIN discord_guilds g ON g.guild_id = f.guild_id WHERE f.id = $1 AND f.forum_type = 'workspace'`, forumID).
		Scan(&guildID, &channelID, &ownerID, &botID, &bindingStatus)
	if err != nil {
		return err
	}
	viewRead := discord.PermissionViewChannel | discord.PermissionReadMessageHistory
	operate := viewRead | discord.PermissionSendMessages | discord.PermissionCreatePublicThreads |
		discord.PermissionSendMessagesInThreads | discord.PermissionAttachFiles | discord.PermissionEmbedLinks
	ownerPermission := PermissionSpec{ID: ownerID, Type: "member", Allow: int64(operate)}
	if bindingStatus == "inactive" {
		ownerPermission.Allow = int64(viewRead)
		ownerPermission.Deny = int64(discord.PermissionSendMessages |
			discord.PermissionCreatePublicThreads | discord.PermissionCreatePrivateThreads |
			discord.PermissionSendMessagesInThreads)
	}
	permissions := []PermissionSpec{
		{ID: guildID, Type: "role", Deny: int64(discord.PermissionViewChannel)},
		ownerPermission,
		{ID: botID, Type: "member", Allow: int64(operate | discord.PermissionManageChannels | discord.PermissionManageThreads | discord.PermissionManageMessages)},
	}
	rows, err := store.QueryContext(ctx, `SELECT discord_user_id, access_level FROM discord_forum_access
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
		if level == AccessOperator && bindingStatus == "active" {
			permission.Allow = int64(operate)
		} else {
			permission.Deny = int64(discord.PermissionSendMessages |
				discord.PermissionCreatePublicThreads | discord.PermissionCreatePrivateThreads |
				discord.PermissionSendMessagesInThreads)
		}
		permissions = append(permissions, permission)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return enqueueDiscordOutbox(ctx, store, "forum-permissions:"+forumID.String(),
		"channel.permissions", "channels/"+channelID,
		map[string]any{"channelId": channelID, "permissions": permissions}, "")
}
