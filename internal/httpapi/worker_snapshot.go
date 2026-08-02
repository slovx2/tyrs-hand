package httpapi

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) loadWorkerSnapshot(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (workerprotocol.TaskSnapshot, error) {
	var result workerprotocol.TaskSnapshot
	provider, err := s.settings.AgentProvider(ctx)
	if err != nil {
		return result, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT name, COALESCE(model,''),
		COALESCE(reasoning_effort,''), COALESCE(service_tier,''), sandbox,
		approval_policy, network_enabled FROM agent_profiles WHERE id = $1`,
		claimed.AgentProfileID).Scan(&result.Runtime.ProfileName, &result.Runtime.Model,
		&result.Runtime.ReasoningEffort, &result.Runtime.ServiceTier, &result.Runtime.Sandbox,
		&result.Runtime.ApprovalPolicy, &result.Runtime.NetworkEnabled)
	if err != nil {
		return result, err
	}
	result.Runtime.ModelSource = provider.ModelSource
	result.Runtime.BaseURL = provider.BaseURL
	result.Runtime.ProxyURL = provider.ProxyURL
	result.Runtime.ConfigSignature = provider.ConfigSignature
	agents, err := s.settings.GlobalAgents(ctx)
	if err != nil {
		return result, err
	}
	result.Runtime.GlobalAgents = agents.Content
	if claimed.SourceType == codexcontrol.SourceDevelopment {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(model,''),
			COALESCE(reasoning_effort,''), COALESCE(service_tier,'standard'),
			collaboration_mode, settings_revision
			FROM codex_turn_runs WHERE id = $1`, claimed.RunID).
			Scan(&result.Runtime.Model, &result.Runtime.ReasoningEffort,
				&result.Runtime.ServiceTier, &result.Runtime.CollaborationMode,
				&result.Runtime.SettingsRevision); err != nil {
			return result, err
		}
	} else {
		preferences, preferenceErr := s.freezeWorkerRuntimePreferences(ctx, claimed)
		if preferenceErr != nil {
			return result, preferenceErr
		}
		result.Runtime.Model = preferences.Model
		result.Runtime.ReasoningEffort = preferences.ReasoningEffort
		result.Runtime.ServiceTier = preferences.ServiceTier
	}
	if claimed.SourceType == codexcontrol.SourceGitHub {
		result.GitHub, err = s.loadGitHubWorkerSnapshot(ctx, claimed)
	} else {
		result.Development, err = s.loadDevelopmentWorkerSnapshot(ctx, claimed)
		if err == nil && claimed.DiscordConversationID != uuid.Nil &&
			(claimed.DiscordMessageID != "" || claimed.InputSurface == "desktop") {
			result.Discord, err = s.loadDiscordWorkerSnapshot(ctx, claimed)
		}
	}
	return result, err
}

func (s *Server) freezeWorkerRuntimePreferences(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (codexsettings.EffectivePreferences, error) {
	var result codexsettings.EffectivePreferences
	var model, effort, tier sql.NullString
	var frozen sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT model, reasoning_effort, service_tier,
		runtime_preferences_frozen_at FROM codex_thread_controls WHERE id = $1`, claimed.ControlID).
		Scan(&model, &effort, &tier, &frozen)
	if err != nil {
		return result, err
	}
	if frozen.Valid {
		result.Model, result.ReasoningEffort = model.String, effort.String
		result.ServiceTier = tier.String
		if result.ServiceTier == "" && claimed.InputSurface != "desktop" {
			result.ServiceTier = "standard"
		}
		return result, nil
	}
	if claimed.SourceType == codexcontrol.SourceDevelopment {
		err = s.db.QueryRowContext(ctx, `SELECT COALESCE(model,''),
			COALESCE(reasoning_effort,''), COALESCE(service_tier,'standard')
			FROM development_sessions WHERE id = $1`, claimed.SessionID).
			Scan(&result.Model, &result.ReasoningEffort, &result.ServiceTier)
	} else {
		result, err = codexsettings.NewService(s.db).Resolve(ctx, claimed.RepositoryID,
			uuid.Nil, claimed.AgentProfileID)
	}
	if err != nil {
		return codexsettings.EffectivePreferences{}, err
	}
	err = s.db.QueryRowContext(ctx, `UPDATE codex_thread_controls SET model = NULLIF($2,''),
		reasoning_effort = NULLIF($3,''), service_tier = $4,
		runtime_preferences_frozen_at = now(), updated_at = now()
		WHERE id = $1 AND runtime_preferences_frozen_at IS NULL
		RETURNING COALESCE(model,''), COALESCE(reasoning_effort,''), service_tier`,
		claimed.ControlID, result.Model, result.ReasoningEffort, result.ServiceTier).
		Scan(&result.Model, &result.ReasoningEffort, &result.ServiceTier)
	if errors.Is(err, sql.ErrNoRows) {
		return s.freezeWorkerRuntimePreferences(ctx, claimed)
	}
	return result, err
}

func (s *Server) loadDevelopmentWorkerSnapshot(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (*workerprotocol.DevelopmentSnapshot, error) {
	result := workerprotocol.DevelopmentSnapshot{
		SessionID: claimed.SessionID, MessageID: claimed.ID.String(), Body: claimed.Instruction,
		ParticipantID: claimed.ActorParticipantID, DisplayName: claimed.ActorDisplayName,
		InputSurface: claimed.InputSurface, Development: &workerprotocol.DevelopmentSpec{},
	}
	if claimed.DiscordMessageID != "" {
		result.MessageID = claimed.DiscordMessageID
	}
	var forumID, conversationID sql.NullString
	development := result.Development
	err := s.db.QueryRowContext(ctx, `SELECT environment.id, forum.id::text,
		conversation.id::text, 'ready', project.relative_path, COALESCE(project.branch,''),
		project.project_kind, project.id, project.name, COALESCE(project.remote_url,''),
		COALESCE(project.branch,''), environment.status, COALESCE(environment.image_ref,''),
		COALESCE(environment.image_id,''), environment.container_name,
		COALESCE(environment.container_id,''), environment.data_volume_name,
		environment.home_volume_name, environment.network_name,
		COALESCE(environment.runtime_user,''), COALESCE(environment.runtime_uid,0),
		COALESCE(environment.runtime_gid,0), COALESCE(environment.runtime_home,'')
		FROM development_sessions session
		JOIN discord_development_environments environment
			ON environment.id=session.development_environment_id
		JOIN development_projects project ON project.id=session.development_project_id
		LEFT JOIN discord_conversations conversation ON conversation.session_id=session.id
		LEFT JOIN discord_forums forum ON forum.id=conversation.forum_id
		WHERE session.id=$1 AND project.availability_status='available'
		  AND environment.status='running'`, claimed.SessionID).Scan(
		&development.EnvironmentID, &forumID, &conversationID,
		&development.WorkspaceStatus, &development.WorkspaceRelative,
		&development.WorkspaceBranch, &development.WorkspaceKind, &development.ProjectID,
		&development.Repository, &development.CloneURL, &development.DefaultRef,
		&development.EnvironmentStatus, &development.ImageRef, &development.ImageID,
		&development.ContainerName, &development.ContainerID, &development.DataVolume,
		&development.HomeVolume, &development.Network, &development.RuntimeUser,
		&development.RuntimeUID, &development.RuntimeGID, &development.RuntimeHome)
	if err != nil {
		return nil, err
	}
	if forumID.Valid {
		development.ForumID, err = uuid.Parse(forumID.String)
		if err != nil {
			return nil, err
		}
	}
	if conversationID.Valid {
		development.ConversationID, err = uuid.Parse(conversationID.String)
		if err != nil {
			return nil, err
		}
	}
	return &result, nil
}

func (s *Server) loadGitHubWorkerSnapshot(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (*workerprotocol.GitHubSnapshot, error) {
	var result workerprotocol.GitHubSnapshot
	err := s.db.QueryRowContext(ctx, `SELECT r.owner, r.name, r.clone_url, r.default_branch,
		w.kind, w.external_number, COALESCE(w.head_sha,''), COALESCE(w.head_ref,''),
		COALESCE(w.head_repository,''), COALESCE(w.base_sha,''), COALESCE(w.base_ref,''),
		COALESCE(w.html_url,'') FROM repositories r JOIN work_items w ON w.repository_id = r.id
		WHERE r.id = $1 AND w.id = $2`, claimed.RepositoryID, claimed.WorkItemID).Scan(
		&result.Owner, &result.Repository, &result.CloneURL, &result.DefaultBranch,
		&result.Kind, &result.Number, &result.HeadSHA, &result.HeadRef, &result.HeadRepository,
		&result.BaseSHA, &result.BaseRef, &result.HTMLURL)
	return &result, err
}

func (s *Server) loadDiscordWorkerSnapshot(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (*workerprotocol.DiscordSnapshot, error) {
	if claimed.InputSurface == "desktop" {
		return s.loadDesktopDiscordWorkerSnapshot(ctx, claimed)
	}
	var result workerprotocol.DiscordSnapshot
	var bindingID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT c.guild_id, c.thread_id, m.message_id,
		c.owner_discord_user_id, f.id, f.development_environment_id, m.body,
		m.discord_user_id, m.display_name, m.username, COALESCE(m.github_user_id,0),
		COALESCE(m.github_login,''), m.github_binding_id::text, COALESCE(m.binding_version,0),
		m.access_snapshot FROM discord_conversations c
		JOIN discord_input_messages m ON m.conversation_id = c.id
		JOIN discord_forums f ON f.id = c.forum_id AND f.forum_type = 'development'
		WHERE c.id = $1 AND m.message_id = $2`, claimed.DiscordConversationID,
		claimed.DiscordMessageID).Scan(&result.GuildID, &result.ThreadID, &result.MessageID,
		&result.OwnerUserID, &result.ForumID, &result.EnvironmentID, &result.Body,
		&result.UserID, &result.DisplayName, &result.Username, &result.GitHubUserID,
		&result.GitHubLogin, &bindingID, &result.BindingVersion, &result.Access)
	if err != nil {
		return nil, err
	}
	if claimed.Instruction != "" {
		result.Body = claimed.Instruction
	}
	result.BindingID = bindingID.String
	var development workerprotocol.DevelopmentSpec
	var projectID sql.NullString
	development.ConversationID = claimed.DiscordConversationID
	err = s.db.QueryRowContext(ctx, `SELECT e.id, f.id, 'ready', project.relative_path,
		COALESCE(project.branch,''), project.project_kind,
		project.id::text, project.name, COALESCE(project.remote_url,''), '',
		e.status, COALESCE(e.image_ref,''), COALESCE(e.image_id,''), e.container_name,
		COALESCE(e.container_id,''), e.data_volume_name, e.home_volume_name, e.network_name,
		COALESCE(e.runtime_user,''), COALESCE(e.runtime_uid,0), COALESCE(e.runtime_gid,0),
		COALESCE(e.runtime_home,'')
		FROM discord_forums f
		JOIN discord_development_environments e ON e.id = f.development_environment_id
		JOIN development_projects project ON project.id=f.development_project_id
		WHERE f.id = $1 AND f.binding_status = 'active'
			AND project.availability_status = 'available' AND e.status = 'running'`, result.ForumID).
		Scan(&development.EnvironmentID, &development.ForumID,
			&development.WorkspaceStatus, &development.WorkspaceRelative,
			&development.WorkspaceBranch, &development.WorkspaceKind, &projectID,
			&development.Repository, &development.CloneURL,
			&development.DefaultRef, &development.EnvironmentStatus,
			&development.ImageRef, &development.ImageID, &development.ContainerName,
			&development.ContainerID, &development.DataVolume, &development.HomeVolume,
			&development.Network, &development.RuntimeUser, &development.RuntimeUID,
			&development.RuntimeGID, &development.RuntimeHome)
	if err != nil {
		return nil, err
	}
	development.ProjectID = parseOptionalUUID(projectID)
	result.Development = &development
	rows, err := s.db.QueryContext(ctx, `SELECT attachment.id, attachment.kind,
		attachment.original_filename, attachment.media_type, attachment.size_bytes,
		COALESCE(attachment.sha256,'') FROM discord_attachments attachment
		JOIN discord_input_messages message ON message.message_id = attachment.message_id
		WHERE message.turn_intent_id = $1 AND attachment.status = 'ready'
		ORDER BY message.received_at DESC, attachment.created_at DESC, attachment.id DESC
		LIMIT $2`, claimed.ID, discordintegration.DefaultMaxAttachments)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item workerprotocol.Attachment
		if err := rows.Scan(&item.ID, &item.Kind, &item.Filename, &item.MediaType, &item.Size,
			&item.SHA256); err != nil {
			return nil, err
		}
		result.Attachments = append(result.Attachments, item)
	}
	return &result, rows.Err()
}

func (s *Server) loadDesktopDiscordWorkerSnapshot(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (*workerprotocol.DiscordSnapshot, error) {
	result := &workerprotocol.DiscordSnapshot{Body: claimed.Instruction, Access: "owner"}
	var sshUserID, sshDisplayName, conversationID string
	err := s.db.QueryRowContext(ctx, `SELECT e.guild_id, COALESCE(c.thread_id,''),
		f.owner_discord_user_id, f.id, e.id, COALESCE(c.id::text,''),
		COALESCE(e.ssh_discord_user_id, ''),
		COALESCE(NULLIF(m.display_name, ''), m.username, '')
		FROM codex_thread_controls ct
		LEFT JOIN desktop_thread_requests request ON request.control_id = ct.id
		LEFT JOIN discord_conversations c ON c.id = ct.discord_conversation_id
		JOIN discord_forums f ON f.id = COALESCE(c.forum_id, request.forum_id)
		JOIN discord_development_environments e ON e.id = ct.development_environment_id
		LEFT JOIN discord_members m ON m.guild_id = e.guild_id
			AND m.discord_user_id = e.ssh_discord_user_id
		WHERE ct.id = $1 AND f.forum_type = 'development'`, claimed.ControlID).
		Scan(&result.GuildID, &result.ThreadID, &result.OwnerUserID, &result.ForumID,
			&result.EnvironmentID, &conversationID, &sshUserID, &sshDisplayName)
	if err != nil {
		return nil, err
	}
	if sshUserID != "" {
		result.UserID = sshUserID
		result.DisplayName = sshDisplayName
	}
	development := &workerprotocol.DevelopmentSpec{}
	var projectID sql.NullString
	development.ConversationID, _ = uuid.Parse(conversationID)
	err = s.db.QueryRowContext(ctx, `SELECT e.id, f.id, 'ready', project.relative_path,
		COALESCE(project.branch,''), project.project_kind,
		project.id::text, project.name, COALESCE(project.remote_url,''), '',
		e.status, COALESCE(e.image_ref,''), COALESCE(e.image_id,''), e.container_name,
		COALESCE(e.container_id,''), e.data_volume_name, e.home_volume_name, e.network_name,
		COALESCE(e.runtime_user,''), COALESCE(e.runtime_uid,0), COALESCE(e.runtime_gid,0),
		COALESCE(e.runtime_home,'')
		FROM discord_forums f
		JOIN discord_development_environments e ON e.id = f.development_environment_id
		JOIN development_projects project ON project.id=f.development_project_id
		WHERE f.id = $1 AND f.binding_status = 'active'
			AND project.availability_status = 'available' AND e.status = 'running'`, result.ForumID).
		Scan(&development.EnvironmentID, &development.ForumID,
			&development.WorkspaceStatus, &development.WorkspaceRelative,
			&development.WorkspaceBranch, &development.WorkspaceKind, &projectID,
			&development.Repository, &development.CloneURL,
			&development.DefaultRef, &development.EnvironmentStatus,
			&development.ImageRef, &development.ImageID, &development.ContainerName,
			&development.ContainerID, &development.DataVolume, &development.HomeVolume,
			&development.Network, &development.RuntimeUser, &development.RuntimeUID,
			&development.RuntimeGID, &development.RuntimeHome)
	if err != nil {
		return nil, err
	}
	development.ProjectID = parseOptionalUUID(projectID)
	result.Development = development
	return result, nil
}
