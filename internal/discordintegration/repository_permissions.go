package discordintegration

import (
	"context"
	"errors"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"
)

type repositoryForumPermission struct {
	ForumID        uuid.UUID
	ChannelID      string
	InstallationID int64
	Owner          string
	Repository     string
}

func (d *Daemon) syncRepositoryPermissions(ctx context.Context, guildID string) error {
	if d.githubPermission == nil {
		return errors.New("github 权限检查器尚未配置")
	}
	forums, err := d.repositoryForums(ctx, guildID)
	if err != nil {
		return err
	}
	rows, err := d.manager.db.QueryContext(ctx, `SELECT discord_user_id, github_login
		FROM discord_identity_bindings WHERE guild_id = $1 AND status = 'active'
		ORDER BY discord_user_id`, guildID)
	if err != nil {
		return err
	}
	bindings := make(map[string]string)
	for rows.Next() {
		var userID, login string
		if err := rows.Scan(&userID, &login); err != nil {
			_ = rows.Close()
			return err
		}
		bindings[userID] = login
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, forum := range forums {
		for userID, login := range bindings {
			permission, permissionErr := d.githubPermission(ctx, forum.InstallationID, forum.Owner, forum.Repository, login)
			allowed := permissionErr == nil && repositoryPermissionRank(permission) >= 1
			if allowed {
				_, err = d.manager.db.ExecContext(ctx, `INSERT INTO discord_forum_access
					(forum_id, discord_user_id, access_level) VALUES ($1, $2, 'readonly')
					ON CONFLICT(forum_id, discord_user_id) DO UPDATE SET access_level = 'readonly', updated_at = now()`,
					forum.ForumID, userID)
			} else {
				_, err = d.manager.db.ExecContext(ctx, `DELETE FROM discord_forum_access
					WHERE forum_id = $1 AND discord_user_id = $2`, forum.ForumID, userID)
			}
			if err != nil {
				return err
			}
		}
		if err := d.manager.syncRepositoryForumPermissions(ctx, guildID, forum); err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) repositoryForums(ctx context.Context, guildID string) ([]repositoryForumPermission, error) {
	rows, err := d.manager.db.QueryContext(ctx, `SELECT f.id, dr.discord_id, i.external_id, r.owner, r.name
		FROM discord_forums f JOIN discord_resources dr ON dr.id = f.resource_id
		JOIN repositories r ON r.id = f.repository_id
		JOIN scm_installations i ON i.id = r.installation_id
		WHERE f.guild_id = $1 AND f.forum_type = 'repository' AND r.enabled = true
		ORDER BY r.owner, r.name`, guildID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []repositoryForumPermission
	for rows.Next() {
		var forum repositoryForumPermission
		if err := rows.Scan(&forum.ForumID, &forum.ChannelID, &forum.InstallationID, &forum.Owner, &forum.Repository); err != nil {
			return nil, err
		}
		result = append(result, forum)
	}
	return result, rows.Err()
}

func (m *Manager) syncRepositoryForumPermissions(ctx context.Context, guildID string, forum repositoryForumPermission) error {
	settings, err := m.Settings(ctx)
	if err != nil {
		return err
	}
	viewRead := discord.PermissionViewChannel | discord.PermissionReadMessageHistory
	denyWrite := discord.PermissionSendMessages | discord.PermissionCreatePublicThreads |
		discord.PermissionSendMessagesInThreads | discord.PermissionCreatePrivateThreads
	botAllow := viewRead | discord.PermissionManageChannels | discord.PermissionManageThreads | discord.PermissionManageMessages
	permissions := []PermissionSpec{
		{ID: guildID, Type: "role", Deny: int64(discord.PermissionViewChannel)},
		{ID: settings.BotUserID, Type: "member", Allow: int64(botAllow)},
	}
	rows, err := m.db.QueryContext(ctx, `SELECT discord_user_id FROM discord_forum_access
		WHERE forum_id = $1 AND access_level = 'readonly' ORDER BY discord_user_id`, forum.ForumID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			_ = rows.Close()
			return err
		}
		permissions = append(permissions, PermissionSpec{ID: userID, Type: "member",
			Allow: int64(viewRead), Deny: int64(denyWrite)})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return NewSQLoutbox(m.db).Enqueue(ctx, "repository-forum-permissions:"+forum.ForumID.String(),
		"channel.permissions", "channels/"+forum.ChannelID,
		map[string]any{"channelId": forum.ChannelID, "permissions": permissions}, "")
}

func repositoryPermissionRank(permission string) int {
	switch permission {
	case "admin":
		return 5
	case "maintain":
		return 4
	case "write", "push":
		return 3
	case "triage":
		return 2
	case "read", "pull":
		return 1
	default:
		return 0
	}
}
