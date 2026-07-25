package discordintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"
)

type Project struct {
	ID                 uuid.UUID `json:"id"`
	Name               string    `json:"name"`
	Status             string    `json:"status"`
	Error              string    `json:"error,omitempty"`
	OwnerDiscordUserID string    `json:"ownerDiscordUserId"`
	OwnerName          string    `json:"ownerName"`
	ForumID            uuid.UUID `json:"forumId"`
	ForumName          string    `json:"forumName,omitempty"`
	DiscordID          string    `json:"discordId,omitempty"`
	EnvironmentID      string    `json:"environmentId,omitempty"`
	WorkspaceStatus    string    `json:"workspaceStatus,omitempty"`
	WorkspaceRelative  string    `json:"workspaceRelative,omitempty"`
	Branch             string    `json:"branch,omitempty"`
	HeadSHA            string    `json:"headSha,omitempty"`
	Dirty              bool      `json:"dirty"`
	InitializationID   string    `json:"initializationId,omitempty"`
}

func (m *Manager) Projects(ctx context.Context) ([]Project, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT p.id, p.name, p.status, COALESCE(p.error,''),
		p.owner_discord_user_id, COALESCE(NULLIF(member.display_name,''), member.username,''),
		p.forum_id, COALESCE(resource.name,''), COALESCE(resource.discord_id,''),
		COALESCE(f.development_environment_id::text,''), COALESCE(workspace.status,''),
		COALESCE(workspace.relative_path,''), COALESCE(workspace.branch,''),
		COALESCE(workspace.head_sha,''), COALESCE(workspace.dirty,false),
		COALESCE((SELECT operation.id::text FROM discord_initialization_operations operation
			WHERE operation.project_id = p.id ORDER BY operation.created_at DESC LIMIT 1),'')
		FROM projects p
		LEFT JOIN discord_members member ON member.guild_id = p.guild_id
			AND member.discord_user_id = p.owner_discord_user_id
		LEFT JOIN discord_forums f ON f.project_id = p.id
		LEFT JOIN discord_resources resource ON resource.id = f.resource_id
		LEFT JOIN discord_forum_workspaces workspace ON workspace.forum_id = f.id
		ORDER BY lower(p.name), p.created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]Project, 0)
	for rows.Next() {
		var item Project
		if err := rows.Scan(&item.ID, &item.Name, &item.Status, &item.Error,
			&item.OwnerDiscordUserID, &item.OwnerName, &item.ForumID, &item.ForumName,
			&item.DiscordID, &item.EnvironmentID, &item.WorkspaceStatus,
			&item.WorkspaceRelative, &item.Branch, &item.HeadSHA, &item.Dirty,
			&item.InitializationID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (m *Manager) CreateProject(ctx context.Context, remoteGuild RemoteGuild, name, ownerID string,
	administratorID uuid.UUID,
) (uuid.UUID, uuid.UUID, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 80 {
		return uuid.Nil, uuid.Nil, errors.New("项目名称必须是 1 到 80 个字符")
	}
	projectID, forumID := uuid.New(), uuid.New()
	plan, err := m.projectInitializationPlan(ctx, remoteGuild, projectID, forumID, name, ownerID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	encoded, err := json.Marshal(plan.Preflight)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO projects
		(id, guild_id, owner_discord_user_id, forum_id, name, requested_by)
		VALUES ($1,$2,$3,$4,$5,$6)`, projectID, plan.Preflight.GuildID, ownerID, forumID,
		name, administratorID); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	operationID, err := m.createInitializationTx(ctx, tx, administratorID, projectID,
		plan, "", encoded)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return projectID, operationID, tx.Commit()
}

func (m *Manager) RetryProject(ctx context.Context, remoteGuild RemoteGuild, projectID,
	administratorID uuid.UUID,
) (uuid.UUID, error) {
	var name, ownerID string
	var forumID uuid.UUID
	if err := m.db.QueryRowContext(ctx, `SELECT name, owner_discord_user_id, forum_id
		FROM projects WHERE id=$1 AND status='error'`, projectID).
		Scan(&name, &ownerID, &forumID); err != nil {
		return uuid.Nil, errors.New("只有创建失败的项目可以重试")
	}
	plan, err := m.projectInitializationPlan(ctx, remoteGuild, projectID, forumID, name, ownerID)
	if err != nil {
		return uuid.Nil, err
	}
	encoded, err := json.Marshal(plan.Preflight)
	if err != nil {
		return uuid.Nil, err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE projects SET status='provisioning', error=NULL,
		updated_at=now() WHERE id=$1 AND status='error'`, projectID)
	if err != nil {
		return uuid.Nil, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return uuid.Nil, errors.New("项目状态已经变化，请刷新后重试")
	}
	operationID, err := m.createInitializationTx(ctx, tx, administratorID, projectID,
		plan, "", encoded)
	if err != nil {
		return uuid.Nil, err
	}
	return operationID, tx.Commit()
}

func (m *Manager) projectInitializationPlan(ctx context.Context, remoteGuild RemoteGuild,
	projectID, forumID uuid.UUID, name, ownerID string,
) (InitializationPlan, error) {
	settings, err := m.Settings(ctx)
	if err != nil {
		return InitializationPlan{}, err
	}
	if settings.GuildID == "" || settings.BotUserID == "" {
		return InitializationPlan{}, errors.New("创建项目前必须配置 Guild ID 和 Bot User ID")
	}
	var ownerName string
	if err := m.db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(display_name,''), username)
		FROM discord_members WHERE guild_id=$1 AND discord_user_id=$2 AND active=true`,
		settings.GuildID, ownerID).Scan(&ownerName); err != nil {
		return InitializationPlan{}, errors.New("项目 Owner 必须是当前 Guild 的活跃成员")
	}
	categoryKey, categoryID, err := m.availableCodexCategory(ctx, settings.GuildID)
	if err != nil {
		return InitializationPlan{}, err
	}
	desired := make([]ChannelSpec, 0, 2)
	if categoryID == "" {
		var index int
		_, _ = fmt.Sscanf(categoryKey, "category.codex.%02d", &index)
		desired = append(desired, ChannelSpec{Key: categoryKey,
			Name: fmt.Sprintf("Codex 会话 %02d", index), Kind: "category"})
	}
	forumName := projectForumName(remoteGuild, name, projectID)
	allow := discord.PermissionViewChannel | discord.PermissionSendMessages |
		discord.PermissionReadMessageHistory | discord.PermissionCreatePublicThreads |
		discord.PermissionSendMessagesInThreads | discord.PermissionAttachFiles | discord.PermissionEmbedLinks
	botAllow := allow | discord.PermissionManageChannels | discord.PermissionManageThreads |
		discord.PermissionManageMessages
	forum := ChannelSpec{Key: "forum.project." + projectID.String(), ParentKey: categoryKey,
		Name: forumName, Kind: "forum", Topic: "Tyrs Hand 普通项目 · " + ownerName + " · " + name,
		PermissionOverwrites: []PermissionSpec{
			{ID: settings.GuildID, Type: "role", Deny: int64(discord.PermissionViewChannel)},
			{ID: ownerID, Type: "member", Allow: int64(allow)},
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
	plan.Actions = append(plan.Actions, InitializationAction{Kind: "forum.project.record",
		Spec: forum, OwnerUserID: ownerID, ProjectID: projectID.String(), ForumID: forumID.String()})
	return plan, nil
}

func projectForumName(guild RemoteGuild, name string, projectID uuid.UUID) string {
	base := channelName(name)
	shortID := strings.ReplaceAll(projectID.String(), "-", "")[:6]
	if base == "" {
		base = "project-" + shortID
	}
	for _, channel := range guild.Channels {
		if channelName(channel.Name) == base {
			return channelName(base + "-" + shortID)
		}
	}
	return base
}

func (m *Manager) DisableProject(ctx context.Context, projectID, administratorID uuid.UUID) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM projects WHERE id=$1 FOR UPDATE`,
		projectID).Scan(&status); err != nil {
		return err
	}
	if status == "disabled" {
		return nil
	}
	if status != "active" {
		return errors.New("只有已就绪的项目可以停用")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET status='disabled', error=NULL,
		updated_at=now() WHERE id=$1`, projectID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, next_sequence_no,
		COALESCE(discord_conversation_id::text,''), agent_profile_id,
		COALESCE(active_intent_id::text,''), status
		FROM codex_thread_controls WHERE project_id=$1 FOR UPDATE`, projectID)
	if err != nil {
		return err
	}
	type control struct {
		id, profileID uuid.UUID
		sequence      int64
		conversation  string
		activeIntent  string
		status        string
	}
	var controls []control
	for rows.Next() {
		var item control
		if err := rows.Scan(&item.id, &item.sequence, &item.conversation, &item.profileID,
			&item.activeIntent, &item.status); err != nil {
			_ = rows.Close()
			return err
		}
		controls = append(controls, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range controls {
		if item.activeIntent == "" || item.status == "idle" || item.status == "error" {
			continue
		}
		idempotencyKey := "project:disable:" + projectID.String() + ":" + item.id.String() + ":" + item.activeIntent
		_, err = tx.ExecContext(ctx, `INSERT INTO codex_turn_intents
			(control_id, sequence_no, operation, behavior, source_type, input_surface,
			 discord_conversation_id, project_id, agent_profile_id, idempotency_key,
			 instruction, actor_login, actor_permission, reply_policy, reply_status, status)
			VALUES ($1,$2,'interrupt','steer_if_active','discord_conversation',$3,
			 NULLIF($4,'')::uuid,$5,$6,$7,'project disabled by administrator',
			 $8,'owner','silent','skipped','queued') ON CONFLICT(idempotency_key) DO NOTHING`,
			item.id, item.sequence, map[bool]string{true: "desktop", false: "discord"}[item.conversation == ""],
			item.conversation, projectID, item.profileID, idempotencyKey, administratorID.String())
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls
			SET next_sequence_no=next_sequence_no+1, status='stopping', updated_at=now()
			WHERE id=$1`, item.id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents intent SET status='canceled',
		last_error_code='project_disabled', last_error_message='project disabled by administrator',
		finished_at=now(), updated_at=now()
		FROM codex_thread_controls control
		WHERE intent.control_id=control.id AND control.project_id=$1
		AND intent.operation='turn_input' AND intent.status IN
			('queued','retry_wait','placement_pending','dispatching')
		AND intent.id IS DISTINCT FROM control.active_intent_id`, projectID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE discord_input_messages message
		SET status='canceled', processed_at=now()
		WHERE message.status='received' AND EXISTS (
			SELECT 1 FROM codex_turn_intents intent
			JOIN codex_thread_controls control ON control.id=intent.control_id
			WHERE control.project_id=$1 AND intent.discord_message_id=message.message_id
			AND intent.status='canceled' AND intent.last_error_code='project_disabled')`,
		projectID); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) EnableProject(ctx context.Context, projectID uuid.UUID) error {
	result, err := m.db.ExecContext(ctx, `UPDATE projects project SET status='active',
		error=NULL, updated_at=now() FROM discord_forums forum
		JOIN discord_forum_workspaces workspace ON workspace.forum_id=forum.id
		JOIN discord_resources resource ON resource.id=forum.resource_id
		WHERE project.id=$1 AND project.status='disabled' AND forum.project_id=project.id
		AND workspace.status='ready' AND resource.status='active'`, projectID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("项目未停用，或 Forum 和工作区尚未就绪")
	}
	return nil
}
